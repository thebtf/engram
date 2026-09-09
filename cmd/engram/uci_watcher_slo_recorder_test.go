package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/handlers/codeintel"
	"github.com/thebtf/engram/internal/uci"
	acceptance "github.com/thebtf/engram/tests/uci/acceptance"
)

const (
	uciWatcherSLORecordEnabledEnv          = "ENGRAM_UCI_WATCHER_SLO_RECORD_ENABLED"
	uciWatcherSLORecordDatabaseDSNEnv      = "ENGRAM_UCI_WATCHER_SLO_DATABASE_DSN"
	uciWatcherSLORecordPathEnv             = "ENGRAM_UCI_WATCHER_SLO_RECORD_PATH"
	uciWatcherSLORecordTimeout             = 20 * time.Minute
	uciWatcherSLORecordCommand             = "go test ./cmd/engram -run '^TestUCIRecordInstalledWatcherSLO$' -count=1 -v -timeout=25m"
	uciWatcherSLORecordRequiredWarmBatches = 100
	uciWatcherSLORecordMaximumAttempts     = 150
)

var errUCIWatcherSLOEmbeddingTerminal = errors.New("installed embedding worker reported a terminal failure")

const uciWatcherSLOStageDiagnosticsSchemaVersion = "engram.uci-watcher-stage-diagnostics/v1"

type uciWatcherSLOStageSpan struct {
	StartedElapsedNS  int64 `json:"started_elapsed_ns"`
	ReturnedElapsedNS int64 `json:"returned_elapsed_ns"`
	ElapsedNS         int64 `json:"elapsed_ns"`
}

type uciWatcherSLOStagePublication struct {
	SourceDigest   string     `json:"source_digest,omitempty"`
	CheckoutDigest string     `json:"checkout_digest,omitempty"`
	ProfileDigest  string     `json:"profile_digest,omitempty"`
	RunDigest      string     `json:"run_digest,omitempty"`
	ViewID         string     `json:"view_id,omitempty"`
	Generation     int64      `json:"generation,omitempty"`
	ManifestDigest string     `json:"manifest_digest,omitempty"`
	ObservedFSSeq  int64      `json:"observed_fs_seq,omitempty"`
	PublishedAtUTC *time.Time `json:"published_at_utc"`
	ScanStartDBUTC *time.Time `json:"scan_start_db_utc"`
	ScanEndDBUTC   *time.Time `json:"scan_end_db_utc"`
	ScanDurationNS *int64     `json:"scan_duration_ns"`
}

type uciWatcherSLOStageEmbedding struct {
	Coverage        string     `json:"coverage,omitempty"`
	TotalCandidates uint64     `json:"total_candidates,omitempty"`
	ReadyCandidates uint64     `json:"ready_candidates,omitempty"`
	PendingJobs     uint64     `json:"pending_jobs,omitempty"`
	JobState        string     `json:"job_state,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	RetryAfterUTC   *time.Time `json:"retry_after_utc,omitempty"`
}

type uciWatcherSLOStageScannerAggregate struct {
	SourceDigest            string    `json:"source_digest"`
	CheckoutDigest          string    `json:"checkout_digest"`
	ProfileDigest           string    `json:"profile_digest"`
	ObservedFSSeq           int64     `json:"observed_fs_seq"`
	ScanStartedAt           time.Time `json:"scan_started_at"`
	ScanCompletedAt         time.Time `json:"scan_completed_at"`
	GitTopologyDurationNS   int64     `json:"git_topology_duration_ns"`
	GitStatusDurationNS     int64     `json:"git_status_duration_ns"`
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

type uciWatcherSLOScannerAggregate struct {
	SourceID                string    `json:"source_id"`
	CheckoutID              string    `json:"checkout_id"`
	ProfileID               string    `json:"profile_id"`
	ObservedFSSeq           int64     `json:"observed_fs_seq"`
	ScanStartedAt           time.Time `json:"scan_started_at"`
	ScanCompletedAt         time.Time `json:"scan_completed_at"`
	GitTopologyDurationNS   int64     `json:"git_topology_duration_ns"`
	GitStatusDurationNS     int64     `json:"git_status_duration_ns"`
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

func uciWatcherSLOStageScannerAggregateFor(aggregate uciWatcherSLOScannerAggregate) uciWatcherSLOStageScannerAggregate {
	return uciWatcherSLOStageScannerAggregate{
		SourceDigest:            uciInstalledAcceptanceStringDigest(aggregate.SourceID),
		CheckoutDigest:          uciInstalledAcceptanceStringDigest(aggregate.CheckoutID),
		ProfileDigest:           uciInstalledAcceptanceStringDigest(aggregate.ProfileID),
		ObservedFSSeq:           aggregate.ObservedFSSeq,
		ScanStartedAt:           aggregate.ScanStartedAt.UTC(),
		ScanCompletedAt:         aggregate.ScanCompletedAt.UTC(),
		GitTopologyDurationNS:   aggregate.GitTopologyDurationNS,
		GitStatusDurationNS:     aggregate.GitStatusDurationNS,
		GitStagedDurationNS:     aggregate.GitStagedDurationNS,
		GitUntrackedDurationNS:  aggregate.GitUntrackedDurationNS,
		CandidateLoopDurationNS: aggregate.CandidateLoopDurationNS,
		ScanTotalDurationNS:     aggregate.ScanTotalDurationNS,
		ResidualDurationNS:      aggregate.ResidualDurationNS,
		CandidateCount:          aggregate.CandidateCount,
		AdmittedCount:           aggregate.AdmittedCount,
		ExcludedCount:           aggregate.ExcludedCount,
		UnreadableCount:         aggregate.UnreadableCount,
		BytesRead:               aggregate.BytesRead,
	}
}

func (aggregate uciWatcherSLOScannerAggregate) valid() bool {
	return aggregate.SourceID != "" && aggregate.CheckoutID != "" && aggregate.ProfileID != "" && aggregate.ObservedFSSeq >= 0 && !aggregate.ScanStartedAt.IsZero() && !aggregate.ScanCompletedAt.IsZero() && !aggregate.ScanCompletedAt.Before(aggregate.ScanStartedAt)
}

type uciWatcherSLOStageEvent struct {
	Name             string                              `json:"name"`
	Span             uciWatcherSLOStageSpan              `json:"span"`
	Tool             string                              `json:"tool,omitempty"`
	ErrorClass       string                              `json:"error_class,omitempty"`
	ErrorCode        string                              `json:"error_code,omitempty"`
	Publication      *uciWatcherSLOStagePublication      `json:"publication,omitempty"`
	Embedding        *uciWatcherSLOStageEmbedding        `json:"embedding,omitempty"`
	ScannerAggregate *uciWatcherSLOStageScannerAggregate `json:"scanner_aggregate,omitempty"`
}

type uciWatcherSLOStagePublicationBounds struct {
	LowerElapsedNS int64 `json:"lower_elapsed_ns"`
	UpperElapsedNS int64 `json:"upper_elapsed_ns"`
}

type uciWatcherSLOStageAttempt struct {
	ID                string                               `json:"id"`
	Sequence          int                                  `json:"sequence"`
	Warmth            string                               `json:"warmth"`
	Status            string                               `json:"status,omitempty"`
	FailingStage      string                               `json:"failing_stage,omitempty"`
	FailureClass      string                               `json:"failure_class,omitempty"`
	CompleteV2Batch   bool                                 `json:"complete_v2_batch"`
	Baseline          uciWatcherSLOStagePublication        `json:"baseline"`
	PublicationBounds *uciWatcherSLOStagePublicationBounds `json:"publication_bounds,omitempty"`
	Events            []uciWatcherSLOStageEvent            `json:"events,omitempty"`
}

type uciWatcherSLOStageRun struct {
	CandidateBranch   string `json:"candidate_branch,omitempty"`
	CandidateCommit   string `json:"candidate_commit,omitempty"`
	CandidateTree     string `json:"candidate_tree,omitempty"`
	Attempted         int    `json:"attempted,omitempty"`
	Finished          int    `json:"finished,omitempty"`
	HealthyWarm       int    `json:"healthy_warm,omitempty"`
	Status            string `json:"status,omitempty"`
	FailureClass      string `json:"failure_class,omitempty"`
	AcceptedV2Written bool   `json:"accepted_v2_written"`
}

type uciWatcherSLOStageJournalRecord struct {
	SchemaVersion string                     `json:"schema_version"`
	Record        string                     `json:"record"`
	TimestampUTC  time.Time                  `json:"timestamp_utc"`
	ElapsedNS     int64                      `json:"elapsed_ns"`
	Run           *uciWatcherSLOStageRun     `json:"run,omitempty"`
	Attempt       *uciWatcherSLOStageAttempt `json:"attempt,omitempty"`
}

type uciWatcherSLOStageJournal struct {
	mu     sync.Mutex
	file   *os.File
	origin time.Time
}

func uciNewWatcherSLOStageJournal(recordPath string) (*uciWatcherSLOStageJournal, error) {
	path := recordPath + ".attempts.jsonl"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &uciWatcherSLOStageJournal{file: file, origin: time.Now()}, nil
}

func (journal *uciWatcherSLOStageJournal) append(record string, run *uciWatcherSLOStageRun, attempt *uciWatcherSLOStageAttempt) error {
	if journal == nil || journal.file == nil {
		return errors.New("installed watcher stage journal is unavailable")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	now := time.Now()
	payload, err := json.Marshal(uciWatcherSLOStageJournalRecord{
		SchemaVersion: uciWatcherSLOStageDiagnosticsSchemaVersion,
		Record:        record,
		TimestampUTC:  now.UTC(),
		ElapsedNS:     now.Sub(journal.origin).Nanoseconds(),
		Run:           run,
		Attempt:       attempt,
	})
	if err != nil {
		return err
	}
	if _, err := journal.file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return journal.file.Sync()
}

func (journal *uciWatcherSLOStageJournal) close() error {
	if journal == nil || journal.file == nil {
		return nil
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	err := journal.file.Close()
	journal.file = nil
	return err
}

func uciWatcherSLOStagePublicationFor(publication uciInstalledAcceptancePublication) uciWatcherSLOStagePublication {
	stage := uciWatcherSLOStagePublication{ViewID: publication.viewID, Generation: publication.generation}
	if publication.sourceID != "" {
		stage.SourceDigest = uciInstalledAcceptanceStringDigest(publication.sourceID)
	}
	if publication.checkoutID != "" {
		stage.CheckoutDigest = uciInstalledAcceptanceStringDigest(publication.checkoutID)
	}
	if publication.profileID != "" {
		stage.ProfileDigest = uciInstalledAcceptanceStringDigest(publication.profileID)
	}
	if publication.runID != "" {
		stage.RunDigest = uciInstalledAcceptanceStringDigest(publication.runID)
	}
	return stage
}

type uciWatcherSLODurableViewRow struct {
	ViewID         string     `gorm:"column:view_id"`
	Generation     int64      `gorm:"column:generation"`
	ManifestDigest string     `gorm:"column:manifest_digest"`
	ObservedFSSeq  int64      `gorm:"column:observed_fs_seq"`
	PublishedAt    *time.Time `gorm:"column:published_at"`
	ScanStart      *time.Time `gorm:"column:scan_start"`
	ScanEnd        *time.Time `gorm:"column:scan_end"`
}

func uciWatcherSLOStagePublicationForDurableView(row uciWatcherSLODurableViewRow) (uciWatcherSLOStagePublication, error) {
	stage := uciWatcherSLOStagePublication{ViewID: row.ViewID, Generation: row.Generation, ManifestDigest: row.ManifestDigest, ObservedFSSeq: row.ObservedFSSeq}
	if row.PublishedAt != nil && !row.PublishedAt.IsZero() {
		publishedAt := row.PublishedAt.UTC()
		stage.PublishedAtUTC = &publishedAt
	}
	if row.ScanStart != nil {
		if row.ScanStart.IsZero() {
			return uciWatcherSLOStagePublication{}, errors.New("durable View scan_start is zero")
		}
		scanStart := row.ScanStart.UTC()
		stage.ScanStartDBUTC = &scanStart
	}
	if row.ScanEnd != nil {
		if row.ScanEnd.IsZero() {
			return uciWatcherSLOStagePublication{}, errors.New("durable View scan_end is zero")
		}
		scanEnd := row.ScanEnd.UTC()
		stage.ScanEndDBUTC = &scanEnd
	}
	if stage.ScanStartDBUTC != nil && stage.ScanEndDBUTC != nil {
		if stage.ScanEndDBUTC.Before(*stage.ScanStartDBUTC) {
			return uciWatcherSLOStagePublication{}, errors.New("durable View scan window is inverted")
		}
		duration := stage.ScanEndDBUTC.Sub(*stage.ScanStartDBUTC).Nanoseconds()
		stage.ScanDurationNS = &duration
	}
	return stage, nil
}

func uciWatcherSLOStageSpanFor(origin, started, returned time.Time) uciWatcherSLOStageSpan {
	return uciWatcherSLOStageSpan{
		StartedElapsedNS:  started.Sub(origin).Nanoseconds(),
		ReturnedElapsedNS: returned.Sub(origin).Nanoseconds(),
		ElapsedNS:         returned.Sub(started).Nanoseconds(),
	}
}

func uciWatcherSLOStageEmbeddingFor(status uciRealCorpusEmbeddingStatus) uciWatcherSLOStageEmbedding {
	stage := uciWatcherSLOStageEmbedding{
		Coverage:        uciRealCorpusEmbeddingSafeCoverage(status.Embedding.Coverage),
		TotalCandidates: status.Embedding.TotalCandidates,
		ReadyCandidates: status.Embedding.ReadyCandidates,
		PendingJobs:     status.Embedding.PendingJobs,
		JobState:        uciRealCorpusEmbeddingSafeJobState(status.Embedding.JobState),
	}
	if status.Embedding.ErrorCode != nil {
		stage.ErrorCode = *status.Embedding.ErrorCode
	}
	if status.Embedding.RetryAfter != nil && !status.Embedding.RetryAfter.IsZero() {
		retryAfter := status.Embedding.RetryAfter.UTC()
		stage.RetryAfterUTC = &retryAfter
	}
	return stage
}

func uciWatcherSLODiagnosticErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if uciInstalledAcceptanceIsTransientContextMismatch(err) {
		return "CONTEXT_MISMATCH"
	}
	var mcpErr *uciInstalledAcceptanceMCPError
	if errors.As(err, &mcpErr) {
		return mcpErr.code
	}
	return "error"
}

func uciWatcherSLODiagnosticFailureClass(stage string, err error, embedding uciWatcherSLOStageEmbedding) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if stage == "embedding" {
		switch embedding.JobState {
		case "retry_scheduled":
			return "embedding_retry_observed"
		case "failed_terminal", "cancelled", "obsolete":
			return "embedding_terminal_failure"
		}
	}
	switch stage {
	case "setup":
		return "setup_failure"
	case "save":
		return "save_failure"
	case "watcher", "barrier", "quiescence", "post_endpoint_barrier", "post_endpoint_quiescence":
		return "publication_failure"
	case "search":
		return "search_failure"
	default:
		return "installed_transport_failure"
	}
}

func uciWatcherSLODiagnosticMigrationWarningClass(observed bool) string {
	if observed {
		return "setup_migration_warning"
	}
	return ""
}

type uciWatcherSLOStatusObservation struct {
	publication uciInstalledAcceptancePublication
	embedding   uciWatcherSLOStageEmbedding
	span        uciWatcherSLOStageSpan
}

type uciWatcherSLODurableObservation struct {
	publication uciInstalledAcceptancePublication
	stage       uciWatcherSLOStagePublication
	span        uciWatcherSLOStageSpan
}

type uciWatcherSLOAttemptTrace struct {
	mu                     sync.Mutex
	origin                 time.Time
	baseline               uciInstalledAcceptancePublication
	selected               uciInstalledAcceptancePublication
	hasSelected            bool
	saveStarted            time.Time
	firstClientObserved    time.Time
	firstDurableObserved   time.Time
	events                 []uciWatcherSLOStageEvent
	statusObservations     []uciWatcherSLOStatusObservation
	durableObservations    []uciWatcherSLODurableObservation
	scannerObservations    []uciWatcherSLOScannerAggregateObservation
	lastDurableNegative    time.Time
	lastEmbeddingSignature string
}

func newUCIWatcherSLOAttemptTrace(origin time.Time, baseline uciInstalledAcceptancePublication) *uciWatcherSLOAttemptTrace {
	return &uciWatcherSLOAttemptTrace{origin: origin, baseline: baseline, events: make([]uciWatcherSLOStageEvent, 0, 32), scannerObservations: make([]uciWatcherSLOScannerAggregateObservation, 0, 8)}
}

func (trace *uciWatcherSLOAttemptTrace) add(event uciWatcherSLOStageEvent) {
	if trace == nil {
		return
	}
	if len(trace.events) < 128 {
		trace.events = append(trace.events, event)
	}
}

func uciWatcherSLOPublicationFromStatus(status uciInstalledAcceptanceStatus) (uciInstalledAcceptancePublication, bool) {
	if status.context == nil || status.context.sourceID == "" || status.context.checkoutID == "" || status.context.profileID == "" || status.context.viewID == "" || status.context.generation < 1 || status.runID == "" || status.freshness == nil || status.freshness.state != "observed_current" {
		return uciInstalledAcceptancePublication{}, false
	}
	return uciInstalledAcceptancePublication{
		sourceID:       status.context.sourceID,
		checkoutID:     status.context.checkoutID,
		profileID:      status.context.profileID,
		viewID:         status.context.viewID,
		generation:     status.context.generation,
		runID:          status.runID,
		freshnessState: status.freshness.state,
	}, true
}

func uciWatcherSLOSamePublicationContext(left, right uciInstalledAcceptancePublication) bool {
	return left.sourceID == right.sourceID && left.checkoutID == right.checkoutID && left.profileID == right.profileID
}

func uciWatcherSLOSamePublicationIdentity(left, right uciInstalledAcceptancePublication) bool {
	return uciWatcherSLOSamePublicationContext(left, right) && left.viewID == right.viewID && left.generation == right.generation
}

func uciWatcherSLOEmbeddingSignature(embedding uciWatcherSLOStageEmbedding) string {
	retryAfter := ""
	if embedding.RetryAfterUTC != nil {
		retryAfter = embedding.RetryAfterUTC.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%s/%d/%d/%d/%s/%s/%s", embedding.Coverage, embedding.TotalCandidates, embedding.ReadyCandidates, embedding.PendingJobs, embedding.JobState, embedding.ErrorCode, retryAfter)
}

func (trace *uciWatcherSLOAttemptTrace) recordExactStatus(observation uciWatcherSLOStatusObservation) {
	if !trace.hasSelected || !uciWatcherSLOSamePublicationIdentity(observation.publication, trace.selected) {
		return
	}
	publication := uciWatcherSLOStagePublicationFor(observation.publication)
	if !trace.hasEvent("client_view_first_seen") {
		trace.firstClientObserved = trace.origin.Add(time.Duration(observation.span.ReturnedElapsedNS))
		trace.add(uciWatcherSLOStageEvent{Name: "client_view_first_seen", Span: observation.span, Publication: &publication})
	}
	signature := uciWatcherSLOEmbeddingSignature(observation.embedding)
	if signature != trace.lastEmbeddingSignature {
		trace.lastEmbeddingSignature = signature
		embedding := observation.embedding
		trace.add(uciWatcherSLOStageEvent{Name: "embedding_status_observed", Span: observation.span, Publication: &publication, Embedding: &embedding})
	}
	if !trace.hasEvent("embedding_ready_first_seen") && uciWatcherSLOStageEmbeddingReady(observation.embedding) {
		embedding := observation.embedding
		trace.add(uciWatcherSLOStageEvent{Name: "embedding_ready_first_seen", Span: observation.span, Publication: &publication, Embedding: &embedding})
	}
}

func (trace *uciWatcherSLOAttemptTrace) hasEvent(name string) bool {
	for _, event := range trace.events {
		if event.Name == name {
			return true
		}
	}
	return false
}

func uciWatcherSLOStageEmbeddingReady(embedding uciWatcherSLOStageEmbedding) bool {
	return embedding.ErrorCode == "" && embedding.Coverage == "complete" && embedding.TotalCandidates > 0 && embedding.ReadyCandidates == embedding.TotalCandidates && embedding.PendingJobs == 0 && embedding.JobState == "succeeded"
}

func (trace *uciWatcherSLOAttemptTrace) observeTool(name string, started, returned time.Time, payload json.RawMessage, err error) {
	if trace == nil || (name != "codebase_status" && name != "codebase_search") {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	span := uciWatcherSLOStageSpanFor(trace.origin, started, returned)
	if err != nil {
		trace.add(uciWatcherSLOStageEvent{Name: name + "_return", Span: span, Tool: name, ErrorClass: "mcp_error", ErrorCode: uciWatcherSLODiagnosticErrorCode(err)})
		return
	}
	if name == "codebase_search" {
		trace.add(uciWatcherSLOStageEvent{Name: "search_return", Span: span, Tool: name})
		return
	}
	status, statusErr := uciDecodeInstalledAcceptanceStatus(payload)
	if statusErr != nil {
		trace.add(uciWatcherSLOStageEvent{Name: "status_return", Span: span, Tool: name, ErrorClass: "decode_error", ErrorCode: "invalid_status"})
		return
	}
	publication, found := uciWatcherSLOPublicationFromStatus(status)
	if !found || !uciWatcherSLOSamePublicationContext(publication, trace.baseline) || uciWatcherSLOSamePublicationIdentity(publication, trace.baseline) {
		return
	}
	var embeddingStatus uciRealCorpusEmbeddingStatus
	if err := json.Unmarshal(payload, &embeddingStatus); err != nil {
		trace.add(uciWatcherSLOStageEvent{Name: "status_return", Span: span, Tool: name, ErrorClass: "decode_error", ErrorCode: "invalid_embedding_status"})
		return
	}
	observation := uciWatcherSLOStatusObservation{publication: publication, embedding: uciWatcherSLOStageEmbeddingFor(embeddingStatus), span: span}
	if len(trace.statusObservations) < 128 {
		trace.statusObservations = append(trace.statusObservations, observation)
	}
	trace.recordExactStatus(observation)
}

func (trace *uciWatcherSLOAttemptTrace) observeStage(stage string, started, returned time.Time, err error) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	startedSpan := uciWatcherSLOStageSpanFor(trace.origin, started, started)
	span := uciWatcherSLOStageSpanFor(trace.origin, started, returned)
	trace.add(uciWatcherSLOStageEvent{Name: stage + "_begin", Span: startedSpan})
	trace.add(uciWatcherSLOStageEvent{Name: stage + "_return", Span: span, ErrorClass: uciWatcherSLODiagnosticFailureClass(stage, err, uciWatcherSLOStageEmbedding{}), ErrorCode: uciWatcherSLODiagnosticErrorCode(err)})
}

func (trace *uciWatcherSLOAttemptTrace) observeCall(stage string, started, returned time.Time, err error) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	startedSpan := uciWatcherSLOStageSpanFor(trace.origin, started, started)
	span := uciWatcherSLOStageSpanFor(trace.origin, started, returned)
	trace.add(uciWatcherSLOStageEvent{Name: stage + "_begin", Span: startedSpan})
	trace.add(uciWatcherSLOStageEvent{Name: stage + "_return", Span: span, ErrorClass: uciWatcherSLODiagnosticFailureClass("search", err, uciWatcherSLOStageEmbedding{}), ErrorCode: uciWatcherSLODiagnosticErrorCode(err)})
}

type uciWatcherSLOScannerAggregateObservation struct {
	aggregate uciWatcherSLOScannerAggregate
	span      uciWatcherSLOStageSpan
}

func (trace *uciWatcherSLOAttemptTrace) observeScannerAggregate(aggregate uciWatcherSLOScannerAggregate, observed time.Time) {
	if trace == nil || !aggregate.valid() {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if len(trace.scannerObservations) < 128 {
		trace.scannerObservations = append(trace.scannerObservations, uciWatcherSLOScannerAggregateObservation{aggregate: aggregate, span: uciWatcherSLOStageSpanFor(trace.origin, observed, observed)})
	}
}

func (trace *uciWatcherSLOAttemptTrace) joinScannerAggregates(view acceptance.UCIWatcherSLOView) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	for _, observation := range trace.scannerObservations {
		aggregate := observation.aggregate
		if aggregate.SourceID != view.Context.SourceID || aggregate.CheckoutID != view.Context.CheckoutID || aggregate.ProfileID != view.Context.AnalysisProfileID || aggregate.ObservedFSSeq != view.AcceptedFSSeq {
			continue
		}
		stage := uciWatcherSLOStageScannerAggregateFor(aggregate)
		trace.add(uciWatcherSLOStageEvent{Name: "prepared_scanner_phase_aggregate", Span: observation.span, ScannerAggregate: &stage})
	}
}

func (trace *uciWatcherSLOAttemptTrace) markSave(started, returned time.Time, err error) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.saveStarted = started
	trace.add(uciWatcherSLOStageEvent{Name: "save_begin", Span: uciWatcherSLOStageSpanFor(trace.origin, started, started)})
	trace.add(uciWatcherSLOStageEvent{Name: "save_return", Span: uciWatcherSLOStageSpanFor(trace.origin, started, returned), ErrorClass: uciWatcherSLODiagnosticFailureClass("save", err, uciWatcherSLOStageEmbedding{}), ErrorCode: uciWatcherSLODiagnosticErrorCode(err)})
}

func (trace *uciWatcherSLOAttemptTrace) setPublication(publication uciInstalledAcceptancePublication) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.selected = publication
	trace.hasSelected = true
	for _, observation := range trace.statusObservations {
		trace.recordExactStatus(observation)
	}
	if !trace.lastDurableNegative.IsZero() {
		trace.add(uciWatcherSLOStageEvent{Name: "durable_view_last_absent", Span: uciWatcherSLOStageSpanFor(trace.origin, trace.lastDurableNegative, trace.lastDurableNegative)})
	}
	for _, observation := range trace.durableObservations {
		if uciWatcherSLOSamePublicationIdentity(observation.publication, publication) && !trace.hasEvent("durable_view_first_seen") {
			trace.firstDurableObserved = trace.origin.Add(time.Duration(observation.span.ReturnedElapsedNS))
			stage := observation.stage
			trace.add(uciWatcherSLOStageEvent{Name: "durable_view_first_seen", Span: observation.span, Publication: &stage})
		}
	}
}

func (trace *uciWatcherSLOAttemptTrace) observeDurable(started, returned time.Time, publication uciInstalledAcceptancePublication, stage uciWatcherSLOStagePublication, found bool, err error) {
	if trace == nil {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	span := uciWatcherSLOStageSpanFor(trace.origin, started, returned)
	if err != nil {
		trace.add(uciWatcherSLOStageEvent{Name: "durable_view_query", Span: span, ErrorClass: "durable_observation_error", ErrorCode: uciWatcherSLODiagnosticErrorCode(err)})
		return
	}
	if !found {
		if !trace.saveStarted.IsZero() && !started.Before(trace.saveStarted) {
			trace.lastDurableNegative = started
		}
		return
	}
	observation := uciWatcherSLODurableObservation{publication: publication, stage: stage, span: span}
	if len(trace.durableObservations) < 128 {
		trace.durableObservations = append(trace.durableObservations, observation)
	}
	if trace.hasSelected && uciWatcherSLOSamePublicationIdentity(publication, trace.selected) && !trace.hasEvent("durable_view_first_seen") {
		trace.firstDurableObserved = trace.origin.Add(time.Duration(span.ReturnedElapsedNS))
		stage := stage
		trace.add(uciWatcherSLOStageEvent{Name: "durable_view_first_seen", Span: span, Publication: &stage})
	}
}

func (trace *uciWatcherSLOAttemptTrace) snapshot() []uciWatcherSLOStageEvent {
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]uciWatcherSLOStageEvent(nil), trace.events...)
}

func uciWatcherSLOPublicationObservationBounds(saveStarted, lastNegative, firstPositive, clientObserved time.Time) (time.Duration, time.Duration, bool) {
	if saveStarted.IsZero() {
		return 0, 0, false
	}
	lower := saveStarted
	if !lastNegative.IsZero() && lastNegative.After(lower) {
		lower = lastNegative
	}
	upper := firstPositive
	if upper.IsZero() || (!clientObserved.IsZero() && clientObserved.Before(upper)) {
		upper = clientObserved
	}
	if upper.IsZero() || upper.Before(lower) {
		return 0, 0, false
	}
	return lower.Sub(saveStarted), upper.Sub(saveStarted), true
}

func (trace *uciWatcherSLOAttemptTrace) publicationBounds() *uciWatcherSLOStagePublicationBounds {
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	lower, upper, ok := uciWatcherSLOPublicationObservationBounds(trace.saveStarted, trace.lastDurableNegative, trace.firstDurableObserved, trace.firstClientObserved)
	if !ok {
		return nil
	}
	return &uciWatcherSLOStagePublicationBounds{LowerElapsedNS: lower.Nanoseconds(), UpperElapsedNS: upper.Nanoseconds()}
}

func uciWatcherSLOFinalizeAttempt(journal *uciWatcherSLOStageJournal, attempt *uciWatcherSLOStageAttempt, trace *uciWatcherSLOAttemptTrace, stage string, attemptErr error, embedding uciWatcherSLOStageEmbedding, batch acceptance.UCIWatcherSLOBatch) error {
	if journal == nil || attempt == nil {
		return attemptErr
	}
	trace.joinScannerAggregates(batch.AAfter)
	attempt.Events = trace.snapshot()
	attempt.PublicationBounds = trace.publicationBounds()
	attempt.CompleteV2Batch = attemptErr == nil && batch.ID != ""
	if attemptErr != nil {
		attempt.Status = "failed"
		if errors.Is(attemptErr, context.DeadlineExceeded) {
			attempt.Status = "timed_out"
		}
		attempt.FailingStage = stage
		attempt.FailureClass = uciWatcherSLODiagnosticFailureClass(stage, attemptErr, embedding)
	} else if batch.Outcome == "failed" {
		attempt.Status = "failed"
		attempt.FailingStage = "embedding"
		attempt.FailureClass = "embedding_terminal_failure"
	} else if batch.Outcome == "degraded" {
		attempt.Status = "degraded"
	} else {
		attempt.Status = "complete"
	}
	if err := journal.append("attempt_end", nil, attempt); err != nil {
		return errors.Join(attemptErr, fmt.Errorf("write installed watcher attempt end: %w", err))
	}
	return attemptErr
}

type uciWatcherSLOScannerAggregateObserver struct {
	mu     sync.Mutex
	server *httptest.Server
	trace  *uciWatcherSLOAttemptTrace
}

func uciNewWatcherSLOScannerAggregateObserver() *uciWatcherSLOScannerAggregateObserver {
	observer := &uciWatcherSLOScannerAggregateObserver{}
	observer.server = httptest.NewServer(http.HandlerFunc(observer.serveHTTP))
	return observer
}

func (observer *uciWatcherSLOScannerAggregateObserver) endpoint() string {
	if observer == nil || observer.server == nil {
		return ""
	}
	return observer.server.URL
}

func (observer *uciWatcherSLOScannerAggregateObserver) close() {
	if observer != nil && observer.server != nil {
		observer.server.Close()
	}
}

func (observer *uciWatcherSLOScannerAggregateObserver) setTrace(trace *uciWatcherSLOAttemptTrace) {
	if observer == nil {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.trace = trace
}

func (observer *uciWatcherSLOScannerAggregateObserver) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	var aggregate uciWatcherSLOScannerAggregate
	if err := decoder.Decode(&aggregate); err != nil || !aggregate.valid() {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	observer.mu.Lock()
	trace := observer.trace
	observer.mu.Unlock()
	if trace != nil {
		trace.observeScannerAggregate(aggregate, time.Now())
	}
	writer.WriteHeader(http.StatusNoContent)
}

type uciWatcherSLODurableViewObserver struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func uciStartWatcherSLODurableViewObserver(ctx context.Context, authority *uciInstalledAcceptanceAuthority, baseline uciInstalledAcceptancePublication, trace *uciWatcherSLOAttemptTrace) (*uciWatcherSLODurableViewObserver, error) {
	if authority == nil || authority.store == nil || baseline.sourceID == "" || baseline.checkoutID == "" || baseline.profileID == "" || baseline.viewID == "" {
		return nil, errors.New("installed watcher durable observer is incomplete")
	}
	observerCtx, cancel := context.WithCancel(ctx)
	observer := &uciWatcherSLODurableViewObserver{cancel: cancel, done: make(chan struct{})}
	poll := func() {
		started := time.Now()
		var row uciWatcherSLODurableViewRow
		err := authority.store.GetDB().WithContext(observerCtx).Raw(`
			SELECT view_id, generation, manifest_digest, observed_fs_seq, published_at, scan_start, scan_end
			FROM ci_views
			WHERE source_id = ? AND checkout_id = ? AND profile_id = ?
				AND (view_id <> ? OR generation > ?)
			ORDER BY generation DESC
			LIMIT 1`, baseline.sourceID, baseline.checkoutID, baseline.profileID, baseline.viewID, baseline.generation).Scan(&row).Error
		returned := time.Now()
		if err != nil {
			trace.observeDurable(started, returned, uciInstalledAcceptancePublication{}, uciWatcherSLOStagePublication{}, false, err)
			return
		}
		if row.ViewID == "" {
			trace.observeDurable(started, returned, uciInstalledAcceptancePublication{}, uciWatcherSLOStagePublication{}, false, nil)
			return
		}
		stage, err := uciWatcherSLOStagePublicationForDurableView(row)
		if err != nil {
			trace.observeDurable(started, returned, uciInstalledAcceptancePublication{}, uciWatcherSLOStagePublication{}, false, err)
			return
		}
		trace.observeDurable(started, returned, uciInstalledAcceptancePublication{sourceID: baseline.sourceID, checkoutID: baseline.checkoutID, profileID: baseline.profileID, viewID: row.ViewID, generation: row.Generation}, stage, true, nil)
	}
	poll()
	go func() {
		defer close(observer.done)
		ticker := time.NewTicker(uciInstalledAcceptanceQuiescencePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-observerCtx.Done():
				return
			case <-ticker.C:
				poll()
			}
		}
	}()
	return observer, nil
}

func (observer *uciWatcherSLODurableViewObserver) stop() {
	if observer == nil {
		return
	}
	observer.cancel()
	<-observer.done
}

// TestUCIRecordInstalledWatcherSLO is the caller-owned, opt-in installed
// recorder command. It builds the exact candidate, saves only a disposable A
// worktree, and records normal-client observations. It never calls the direct
// Admit/Begin/Stage/Finalize publication APIs.
func TestUCIRecordInstalledWatcherSLO(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installed watcher recorder")
	}
	if strings.TrimSpace(os.Getenv(uciWatcherSLORecordEnabledEnv)) != "1" {
		t.Skipf("%s=1 is required to run the installed watcher recorder: %s", uciWatcherSLORecordEnabledEnv, uciWatcherSLORecordCommand)
	}

	recordPath := strings.TrimSpace(os.Getenv(uciWatcherSLORecordPathEnv))
	dsn := strings.TrimSpace(os.Getenv(uciWatcherSLORecordDatabaseDSNEnv))
	provider := &uciInstalledAcceptanceEmbeddingProvider{
		URL:   strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingURLEnv)),
		Model: strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingModelEnv)),
		Key:   strings.TrimSpace(os.Getenv(uciRealCorpusEmbeddingAPIKeyEnv)),
	}
	if recordPath == "" || dsn == "" {
		t.Fatal("installed watcher recorder requires an absolute record path and a disposable PostgreSQL DSN")
	}
	if !filepath.IsAbs(recordPath) {
		t.Fatal("installed watcher record path must be absolute")
	}
	if err := uciValidateInstalledAcceptanceEmbeddingProvider(provider); err != nil {
		t.Fatal(err)
	}
	uciInstalledAcceptanceRequireTestPostgres(t, dsn)

	root := uciInstalledAcceptanceCandidateSourceRoot(t)
	if uciInstalledAcceptancePathOverlaps(root, recordPath) {
		t.Fatal("installed watcher record path must be outside the candidate source tree")
	}
	if clean, err := uciWatcherSLOGitClean(t.Context(), root); err != nil || !clean {
		t.Fatalf("installed watcher recorder requires a clean exact candidate: clean=%t err=%v", clean, err)
	}
	deadline, hasDeadline := t.Deadline()
	if hasDeadline && time.Until(deadline) < uciWatcherSLORecordTimeout+5*time.Minute {
		t.Fatalf("installed watcher recorder needs a test deadline of at least %s; rerun with %s", uciWatcherSLORecordTimeout+5*time.Minute, uciWatcherSLORecordCommand)
	}

	journal, err := uciNewWatcherSLOStageJournal(recordPath)
	if err != nil {
		t.Fatalf("create installed watcher stage journal: %v", err)
	}
	scannerAggregateObserver := uciNewWatcherSLOScannerAggregateObserver()
	defer scannerAggregateObserver.close()
	t.Setenv(codeintel.EnvUCIWatcherSLOScannerAggregateObserver, scannerAggregateObserver.endpoint())
	run := &uciWatcherSLOStageRun{}
	if err := journal.append("run_begin", run, nil); err != nil {
		_ = journal.close()
		t.Fatalf("write installed watcher stage run begin: %v", err)
	}
	ended := false
	finish := func(status, failureClass string, acceptedV2Written bool) {
		if ended {
			return
		}
		ended = true
		run.Status = status
		run.FailureClass = failureClass
		run.AcceptedV2Written = acceptedV2Written
		if err := journal.append("run_end", run, nil); err != nil {
			_ = journal.close()
			t.Fatalf("write installed watcher stage run end: %v", err)
		}
		if err := journal.close(); err != nil {
			t.Fatalf("close installed watcher stage journal: %v", err)
		}
	}
	defer func() {
		finish("cancelled", run.FailureClass, false)
	}()

	sandbox := t.TempDir()
	request := uciInstalledAcceptanceRequest{
		Version:                   uciInstalledAcceptanceVersionV1,
		InstallHarnessVersion:     uciInstallHarnessVersionV1,
		CandidateSourceRoot:       root,
		InstallRoot:               filepath.Join(sandbox, "installed watcher recorder"),
		FixtureRoot:               filepath.Join(sandbox, "watcher recorder worktrees"),
		LocalStateRoot:            filepath.Join(sandbox, "watcher recorder state"),
		TestPostgresDSN:           dsn,
		LoopbackHost:              "127.0.0.1",
		ReservedLoopbackPortCount: 2,
		ReadinessTimeout:          30 * time.Second,
		OperationTimeout:          uciWatcherSLORecordTimeout,
		EmbeddingProvider:         provider,
		ScenarioProbePhase:        uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher,
		Fixture: uciInstalledAcceptanceFixture{
			RelativePath:  uciInstalledAcceptanceRelativePath,
			SharedSymbol:  uciInstalledAcceptanceSharedSymbol,
			PrimarySource: uciInstalledAcceptanceAlphaSource,
			LinkedSource:  uciInstalledAcceptanceBetaSource,
			PrimaryCallee: uciInstalledAcceptanceAlphaCallee,
			LinkedCallee:  uciInstalledAcceptanceBetaCallee,
		},
	}
	var input acceptance.UCIWatcherSLOInput
	request.ScenarioProbe = func(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
		measured, err := uciRecordInstalledWatcherSLOObserved(ctx, live, provider, journal, run, scannerAggregateObserver)
		if err != nil {
			return nil, err
		}
		input = measured
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), request.OperationTimeout)
	defer cancel()
	if _, err := runUCIInstalledAcceptance(ctx, request); err != nil {
		failureClass := run.FailureClass
		if failureClass == "" {
			failureClass = uciWatcherSLODiagnosticFailureClass("setup", err, uciWatcherSLOStageEmbedding{})
		}
		finish("failed", failureClass, false)
		t.Fatal(err)
	}
	encoded, err := acceptance.EncodeUCIWatcherSLOInput(input)
	if err != nil {
		finish("failed", "evidence_write_failure", false)
		t.Fatalf("encode installed watcher SLO evidence: %v", err)
	}
	if err := uciWriteInstalledWatcherSLORecord(recordPath, encoded); err != nil {
		finish("failed", "evidence_write_failure", false)
		t.Fatalf("write installed watcher SLO evidence: %v", err)
	}
	report, err := acceptance.CalculateUCIWatcherSLOReport(input)
	if err != nil {
		finish("failed", "evidence_write_failure", true)
		t.Fatalf("account installed watcher SLO evidence: %v", err)
	}
	if !report.Passed {
		finish("failed", "slo_rejected", true)
		t.Fatal("installed watcher SLO evidence did not satisfy the accepted profile")
	}
	finish("completed", "", true)
}

func uciRecordInstalledWatcherSLO(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, provider *uciInstalledAcceptanceEmbeddingProvider) (acceptance.UCIWatcherSLOInput, error) {
	return uciRecordInstalledWatcherSLOObserved(ctx, live, provider, nil, nil, nil)
}

func uciRecordInstalledWatcherSLOObserved(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, provider *uciInstalledAcceptanceEmbeddingProvider, journal *uciWatcherSLOStageJournal, run *uciWatcherSLOStageRun, scannerAggregateObserver *uciWatcherSLOScannerAggregateObserver) (acceptance.UCIWatcherSLOInput, error) {
	if live.Authority == nil || live.Authority.profile == nil || live.Authority.source == nil || live.ClientA == nil || live.ClientB == nil || provider == nil {
		return acceptance.UCIWatcherSLOInput{}, errors.New("installed watcher recorder runtime is incomplete")
	}
	if err := uciWatcherSLOMarkAuxiliaryInactive(ctx, live.Authority); err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	candidate, err := uciWatcherSLOCandidate(ctx, live.Request.CandidateSourceRoot, live.Candidates)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	if journal != nil {
		if run == nil {
			return acceptance.UCIWatcherSLOInput{}, errors.New("installed watcher stage journal has no run state")
		}
		run.CandidateBranch = candidate.Branch
		run.CandidateCommit = candidate.Commit
		run.CandidateTree = candidate.Tree
		if err := journal.append("run_candidate", run, nil); err != nil {
			return acceptance.UCIWatcherSLOInput{}, fmt.Errorf("write installed watcher stage candidate: %w", err)
		}
	}
	selectionA := live.Selections[uciInstalledAcceptanceClientA]
	selectionB := live.Selections[uciInstalledAcceptanceClientB]
	beforeA := live.Publications[uciInstalledAcceptanceClientA]
	beforeB := live.Publications[uciInstalledAcceptanceClientB]
	if selectionA.contextHandle == "" || selectionB.contextHandle == "" || beforeA.viewID == "" || beforeB.viewID == "" {
		return acceptance.UCIWatcherSLOInput{}, errors.New("installed watcher recorder has no A/B baseline")
	}

	batches := make([]acceptance.UCIWatcherSLOBatch, 0, uciWatcherSLORecordMaximumAttempts)
	previousSource, err := os.ReadFile(filepath.Join(live.Worktrees.primaryRoot, filepath.FromSlash(live.Request.Fixture.RelativePath)))
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, fmt.Errorf("read installed watcher A baseline: %w", err)
	}
	healthyWarmBatches := 0
	for sequence := 1; sequence <= uciWatcherSLORecordMaximumAttempts && healthyWarmBatches < uciWatcherSLORecordRequiredWarmBatches; sequence++ {
		warmth := "warm"
		if sequence == 1 {
			warmth = "cold"
		}
		if run != nil {
			run.Attempted++
		}
		batch, nextSource, measureErr := uciRecordInstalledWatcherSLOBatch(ctx, live, selectionA, selectionB, beforeA, beforeB, previousSource, sequence, warmth, journal, scannerAggregateObserver)
		if measureErr != nil {
			return acceptance.UCIWatcherSLOInput{}, measureErr
		}
		if run != nil {
			run.Finished++
		}
		afterA, statusErr := uciWatcherSLOCurrentPublication(ctx, live.ClientA, selectionA)
		if statusErr != nil || afterA.viewID != batch.AAfter.Context.ViewID || afterA.generation != batch.AAfter.Context.Generation {
			return acceptance.UCIWatcherSLOInput{}, errors.New("normal client status changed after watcher evidence was observed")
		}
		batches = append(batches, batch)
		if batch.Warmth == "warm" && batch.Outcome == "healthy" {
			healthyWarmBatches++
			if run != nil {
				run.HealthyWarm++
			}
		}
		previousSource = nextSource
		beforeA = afterA
		selectionA.runID = afterA.runID
	}
	if healthyWarmBatches < uciWatcherSLORecordRequiredWarmBatches {
		return acceptance.UCIWatcherSLOInput{}, fmt.Errorf("installed watcher recorder reached %d attempts with %d healthy warm samples, want %d", len(batches), healthyWarmBatches, uciWatcherSLORecordRequiredWarmBatches)
	}

	finalA := batches[len(batches)-1].AAfter
	environment, profile, err := uciWatcherSLOEnvironmentAndProfile(ctx, live, provider, finalA)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	identity := uci.UCISLOSampleIdentity{Candidate: candidate, Environment: environment}
	for index := range batches {
		batches[index].Identity = identity
	}
	for _, batch := range batches {
		if batch.ChangedBytes > profile.ChangedBytes {
			profile.ChangedBytes = batch.ChangedBytes
		}
	}
	unchanged, err := uciRecordInstalledWatcherSLOUnchanged(ctx, live, selectionA, beforeA, identity)
	if err != nil {
		return acceptance.UCIWatcherSLOInput{}, err
	}
	return acceptance.UCIWatcherSLOInput{
		SchemaVersion:          acceptance.UCIWatcherSLOSchemaVersion,
		Candidate:              candidate,
		Environment:            environment,
		Profile:                profile,
		Batches:                batches,
		UnchangedInputCounters: []acceptance.UCIWatcherSLOUnchangedInputCounter{unchanged},
	}, nil
}

func uciRecordInstalledWatcherSLOBatch(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, selectionA, selectionB uciInstalledAcceptanceSelection, beforeA, beforeB uciInstalledAcceptancePublication, previousSource []byte, sequence int, warmth string, journal *uciWatcherSLOStageJournal, scannerAggregateObserver *uciWatcherSLOScannerAggregateObserver) (batch acceptance.UCIWatcherSLOBatch, nextSource []byte, retErr error) {
	stage := "pre_save"
	var trace *uciWatcherSLOAttemptTrace
	var attempt uciWatcherSLOStageAttempt
	var restoreObserver func()
	var durableObserver *uciWatcherSLODurableViewObserver
	var lastEmbedding uciWatcherSLOStageEmbedding
	if journal != nil {
		attempt = uciWatcherSLOStageAttempt{
			ID:       fmt.Sprintf("installed-watcher-%s-%03d", warmth, sequence),
			Sequence: sequence,
			Warmth:   warmth,
			Baseline: uciWatcherSLOStagePublicationFor(beforeA),
		}
		if err := journal.append("attempt_begin", nil, &attempt); err != nil {
			return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("write installed watcher attempt begin: %w", err)
		}
		trace = newUCIWatcherSLOAttemptTrace(journal.origin, beforeA)
		if scannerAggregateObserver != nil {
			scannerAggregateObserver.setTrace(trace)
			defer scannerAggregateObserver.setTrace(nil)
		}
		restoreObserver = live.ClientA.setToolObserver(trace.observeTool)
		defer func() {
			if restoreObserver != nil {
				restoreObserver()
			}
			durableObserver.stop()
			retErr = uciWatcherSLOFinalizeAttempt(journal, &attempt, trace, stage, retErr, lastEmbedding, batch)
		}()
		var observerErr error
		durableObserver, observerErr = uciStartWatcherSLODurableViewObserver(ctx, live.Authority, beforeA, trace)
		if observerErr != nil {
			return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("start installed watcher durable observer: %w", observerErr)
		}
	}

	aBefore, beforeEmbedding, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selectionA, beforeA)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe A before save: %w", err)
	}
	bBefore, _, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientB, selectionB, beforeB)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe B before A save: %w", err)
	}

	functionName := fmt.Sprintf("UCIWatcherSLO%03d", sequence)
	nextSource = []byte("package fixture\n\nfunc " + live.Request.Fixture.SharedSymbol + "() string { return " + functionName + "() }\nfunc " + functionName + "() string { return \"" + functionName + "\" }\n")
	path := filepath.Join(live.Worktrees.primaryRoot, filepath.FromSlash(live.Request.Fixture.RelativePath))
	stage = "save"
	diagnosticSaveStarted := time.Now()
	started := diagnosticSaveStarted.UTC()
	writeErr := os.WriteFile(path, nextSource, 0o600)
	if trace != nil {
		trace.markSave(diagnosticSaveStarted, time.Now(), writeErr)
	}
	if writeErr != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("save bounded A watcher source: %w", writeErr)
	}
	stage = "watcher"
	publication, response, structuralCompleted, err := uciWatcherSLOAwaitSearchable(ctx, live.ClientA, selectionA, beforeA, functionName, live.Request.Fixture.RelativePath, trace)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, err
	}
	selectionA.runID = publication.runID
	stage = "post_endpoint_barrier"
	barrier, err := uciWatcherSLOAwaitPostEndpointBarrier(ctx, live.ClientA, selectionA, publication, trace)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, err
	}
	stage = "post_endpoint_quiescence"
	if _, err := uciWatcherSLOAwaitPostEndpointQuiescence(ctx, live.ClientA, selectionA, publication, barrier, trace); err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, err
	}
	stage = "embedding"
	embedding, embeddedPublication, embeddingCompleted, embeddingErr := uciWatcherSLOAwaitEmbeddingReady(ctx, live.Authority, live.ClientA, selectionA, publication)
	lastEmbedding = uciWatcherSLOStageEmbeddingFor(embedding)
	terminalEmbeddingFailure := false
	if embeddingErr != nil {
		if !errors.Is(embeddingErr, errUCIWatcherSLOEmbeddingTerminal) {
			return acceptance.UCIWatcherSLOBatch{}, nil, embeddingErr
		}
		terminalEmbeddingFailure = true
		embeddedPublication = publication
	}
	embeddingTiming := acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured}
	if !terminalEmbeddingFailure {
		embeddingTiming = uciWatcherSLOInstalledTiming(acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, started, embeddingCompleted)
	}
	aAfter, afterEmbedding, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selectionA, embeddedPublication)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("observe A after watcher publication: %w", err)
	}
	bAfter, _, err := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientB, selectionB, beforeB)
	if err != nil {
		return acceptance.UCIWatcherSLOBatch{}, nil, fmt.Errorf("independently observe B after A save: %w", err)
	}
	if !uciWatcherSLOViewsEqual(bBefore, bAfter) {
		return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed watcher changed B while only A was saved")
	}
	coverage := "unknown"
	if response.Coverage != nil {
		coverage = string(response.Coverage.Structural)
	}
	outcome, reason := "", ""
	if terminalEmbeddingFailure {
		if !uciWatcherSLOEmbeddingTerminal(afterEmbedding) {
			return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed embedding terminal status did not remain observable")
		}
		outcome, reason = "failed", "embedding_terminal_failure"
	} else {
		if afterEmbedding.Embedding.ReadyCandidates != embedding.Embedding.ReadyCandidates || beforeEmbedding.Embedding.ReadyCandidates > afterEmbedding.Embedding.ReadyCandidates {
			return acceptance.UCIWatcherSLOBatch{}, nil, errors.New("installed watcher embedding counter observation is inconsistent")
		}
		outcome, reason = uciWatcherSLOClassifyOutcome(response.Status, coverage, embedding.Embedding.Coverage)
	}

	return acceptance.UCIWatcherSLOBatch{
		ID:                   fmt.Sprintf("installed-watcher-%s-%03d", warmth, sequence),
		ProfileID:            aAfter.Context.AnalysisProfileID,
		ObservedFSSeq:        aAfter.AcceptedFSSeq,
		ChangedFileCount:     1,
		ChangedBytes:         uciWatcherSLOChangedBytes(previousSource, nextSource),
		ScanOutcome:          uci.IndexScanComplete,
		ResultStatus:         string(response.Status),
		Coverage:             coverage,
		Outcome:              outcome,
		Reason:               reason,
		Warmth:               warmth,
		Scan:                 acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured},
		StructuralFTS:        uciWatcherSLOInstalledTiming(acceptance.UCIWatcherSLOOriginInstalledClientSearch, started, structuralCompleted),
		EmbeddingReadiness:   embeddingTiming,
		LocalACK:             acceptance.UCIWatcherSLOTiming{Origin: acceptance.UCIWatcherSLOOriginUnknownNotMeasured},
		EmbeddingCounters:    acceptance.UCIWatcherSLOEmbeddingCounters{Origin: acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, Measured: true, Before: int64(beforeEmbedding.Embedding.ReadyCandidates), After: int64(afterEmbedding.Embedding.ReadyCandidates)},
		ReembeddedCandidates: int(afterEmbedding.Embedding.ReadyCandidates - beforeEmbedding.Embedding.ReadyCandidates),
		ABefore:              aBefore,
		AAfter:               aAfter,
		BBefore:              bBefore,
		BAfter:               bAfter,
		ABeforeOrigin:        acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		AAfterOrigin:         acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		BBeforeOrigin:        acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
		BAfterOrigin:         acceptance.UCIWatcherSLOOriginInstalledStatusAndView,
	}, nextSource, nil
}

func uciWatcherSLOAwaitSearchable(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, previous uciInstalledAcceptancePublication, functionName, relativePath string, trace *uciWatcherSLOAttemptTrace) (uciInstalledAcceptancePublication, uci.QueryResponse, time.Time, error) {
	for {
		publication, err := uciWaitForInstalledAcceptanceWatcherFirstCurrentPublication(ctx, client, selection, previous)
		if err != nil {
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, fmt.Errorf("await installed watcher discovery: %w", err)
		}
		watched := selection
		watched.runID = publication.runID
		canaryStarted := time.Now()
		response, canaryErr := uciRequireInstalledAcceptanceWatcherCanaryResponse(ctx, client, watched, publication, functionName, relativePath, true)
		canaryCompleted := time.Now()
		trace.observeCall("canary_search", canaryStarted, canaryCompleted, canaryErr)
		if canaryErr != nil {
			if errors.Is(canaryErr, errUCIInstalledAcceptanceWatcherCanaryMissing) {
				previous = publication
				continue
			}
			return uciInstalledAcceptancePublication{}, uci.QueryResponse{}, time.Time{}, fmt.Errorf("verify installed watcher search: %w", canaryErr)
		}
		trace.setPublication(publication)
		return publication, response, canaryCompleted.UTC(), nil
	}
}

func uciWatcherSLOSameExactPublication(left, right uciInstalledAcceptancePublication) bool {
	return uciInstalledAcceptanceSameViewPublication(left, right) && left.runID == right.runID
}

func uciWatcherSLORequirePostEndpointPublication(expected, observed uciInstalledAcceptancePublication) error {
	if !uciWatcherSLOSameExactPublication(expected, observed) {
		return errors.New("post-endpoint safety validation changed the structural View publication")
	}
	return nil
}

func uciWatcherSLOAwaitPostEndpointBarrier(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication, trace *uciWatcherSLOAttemptTrace) (uciInstalledAcceptancePublication, error) {
	selection.runID = expected.runID
	started := time.Now()
	barrier, err := uciWaitForInstalledAcceptanceBarrier(ctx, client, selection)
	if err == nil {
		err = uciWatcherSLORequirePostEndpointPublication(expected, barrier)
	}
	trace.observeStage("post_endpoint_barrier", started, time.Now(), err)
	return barrier, err
}

func uciWatcherSLOAwaitPostEndpointQuiescence(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected, barrier uciInstalledAcceptancePublication, trace *uciWatcherSLOAttemptTrace) (uciInstalledAcceptancePublication, error) {
	selection.runID = expected.runID
	started := time.Now()
	quiescent, err := uciWaitForInstalledAcceptanceQuiescence(ctx, client, selection, barrier)
	if err == nil {
		err = uciWatcherSLORequirePostEndpointPublication(expected, quiescent)
	}
	trace.observeStage("post_endpoint_quiescence", started, time.Now(), err)
	return quiescent, err
}

func uciWatcherSLOEmbeddingTerminal(status uciRealCorpusEmbeddingStatus) bool {
	switch uciRealCorpusEmbeddingSafeJobState(status.Embedding.JobState) {
	case "failed_terminal", "cancelled", "obsolete":
		return true
	default:
		return false
	}
}

func uciWatcherSLOAwaitEmbeddingReady(ctx context.Context, authority *uciInstalledAcceptanceAuthority, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (uciRealCorpusEmbeddingStatus, uciInstalledAcceptancePublication, time.Time, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		view, status, err := uciWatcherSLOCurrentView(ctx, authority, client, selection, expected)
		if err != nil {
			return uciRealCorpusEmbeddingStatus{}, uciInstalledAcceptancePublication{}, time.Time{}, err
		}
		if uciWatcherSLOEmbeddingTerminal(status) {
			return status, uciInstalledAcceptancePublication{}, time.Time{}, errUCIWatcherSLOEmbeddingTerminal
		}
		if uciWatcherSLOEmbeddingReady(status) {
			return status, uciInstalledAcceptancePublication{sourceID: view.Context.SourceID, checkoutID: view.Context.CheckoutID, viewID: view.Context.ViewID, profileID: view.Context.AnalysisProfileID, generation: view.Context.Generation, runID: expected.runID, freshnessState: "observed_current"}, time.Now().UTC(), nil
		}
		select {
		case <-ctx.Done():
			return status, uciInstalledAcceptancePublication{}, time.Time{}, fmt.Errorf("await installed embedding readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func uciWatcherSLOEmbeddingReady(status uciRealCorpusEmbeddingStatus) bool {
	return status.Embedding.ErrorCode == nil &&
		status.Embedding.Coverage == "complete" &&
		status.Embedding.TotalCandidates > 0 &&
		status.Embedding.ReadyCandidates == status.Embedding.TotalCandidates &&
		status.Embedding.PendingJobs == 0 &&
		status.Embedding.JobState != nil && *status.Embedding.JobState == "succeeded"
}

func uciWatcherSLOCurrentView(ctx context.Context, authority *uciInstalledAcceptanceAuthority, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection, expected uciInstalledAcceptancePublication) (acceptance.UCIWatcherSLOView, uciRealCorpusEmbeddingStatus, error) {
	if authority == nil || authority.store == nil || client == nil || selection.contextHandle == "" || expected.runID == "" {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("installed watcher status target is incomplete")
	}
	payload, err := client.Tool(ctx, "codebase_status", map[string]any{"context_handle": selection.contextHandle})
	if err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	status, err := uciDecodeInstalledAcceptanceStatus(payload)
	if err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	publication, err := uciInstalledAcceptanceStatusPublication(status, uciInstalledAcceptanceSelection{contextHandle: selection.contextHandle, runID: expected.runID})
	if err != nil || !uciInstalledAcceptanceSameViewPublication(publication, expected) {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("normal client status did not retain the expected current View")
	}
	var embedding uciRealCorpusEmbeddingStatus
	if err := json.Unmarshal(payload, &embedding); err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	var row struct {
		BuildID        string     `gorm:"column:build_id"`
		ManifestDigest string     `gorm:"column:manifest_digest"`
		ObservedFSSeq  int64      `gorm:"column:observed_fs_seq"`
		PublishedAt    *time.Time `gorm:"column:published_at"`
	}
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT job.job_id AS build_id, view_row.manifest_digest, view_row.observed_fs_seq, view_row.published_at
		FROM ci_views AS view_row
		JOIN ci_jobs AS job ON job.result_view_id = view_row.view_id
		WHERE view_row.view_id = ? AND view_row.source_id = ? AND view_row.checkout_id = ? AND view_row.profile_id = ? AND view_row.generation = ?`,
		publication.viewID, publication.sourceID, publication.checkoutID, publication.profileID, publication.generation).Scan(&row).Error; err != nil {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, err
	}
	if row.BuildID == "" || row.ManifestDigest == "" || row.PublishedAt == nil || row.PublishedAt.IsZero() {
		return acceptance.UCIWatcherSLOView{}, uciRealCorpusEmbeddingStatus{}, errors.New("authoritative durable View fields are incomplete")
	}
	return acceptance.UCIWatcherSLOView{
		BuildID: row.BuildID,
		Context: uci.ContextRef{
			SourceID: publication.sourceID, CheckoutID: publication.checkoutID, ViewID: publication.viewID,
			AnalysisProfileID: publication.profileID, Generation: publication.generation,
		},
		ManifestDigest: uci.IndexDigest(row.ManifestDigest),
		AcceptedFSSeq:  row.ObservedFSSeq,
		PublishedAt:    row.PublishedAt.UTC(),
	}, embedding, nil
}

func uciWatcherSLOCurrentPublication(ctx context.Context, client *uciInstalledAcceptanceMCPClient, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptancePublication, error) {
	status, err := uciInstalledAcceptanceStatusForSelection(ctx, client, selection)
	if err != nil {
		return uciInstalledAcceptancePublication{}, err
	}
	return uciInstalledAcceptanceStatusPublication(status, uciInstalledAcceptanceSelection{contextHandle: selection.contextHandle, runID: status.runID})
}

func uciWatcherSLOEnvironmentAndProfile(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, provider *uciInstalledAcceptanceEmbeddingProvider, finalA acceptance.UCIWatcherSLOView) (uci.UCISLOEnvironment, uci.UCISLOProfile, error) {
	if live.Authority == nil || live.Authority.store == nil || live.Authority.source == nil || live.Authority.profile == nil || provider == nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher environment is incomplete")
	}
	var databaseVersion string
	var databaseSize int64
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw("SHOW server_version").Scan(&databaseVersion).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw(`SELECT COALESCE(SUM(pg_total_relation_size((quote_ident(schemaname) || '.' || quote_ident(tablename))::regclass)), 0)::bigint FROM pg_tables WHERE schemaname = current_schema()`).Scan(&databaseSize).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	textFiles, linesOfCode, err := uciWatcherSLOCorpusCounts(live.Worktrees.primaryRoot)
	if err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	var active, inactive int64
	if err := live.Authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("source_id = ? AND state IN ?", live.Authority.source.SourceID, []gormdb.UCICheckoutState{gormdb.UCICheckoutRegistered, gormdb.UCICheckoutWatching, gormdb.UCICheckoutCatchingUp}).Count(&active).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("source_id = ? AND state = ?", live.Authority.source.SourceID, gormdb.UCICheckoutUnregistered).Count(&inactive).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	if active < 5 || inactive < 1 {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher worktree coverage does not satisfy the accepted profile")
	}
	var persisted struct {
		ProviderRef string `gorm:"column:provider_ref"`
		Model       string `gorm:"column:model"`
	}
	if err := live.Authority.store.GetDB().WithContext(ctx).Raw("SELECT provider_ref, model FROM ci_embedding_profiles WHERE analysis_profile_id = ? ORDER BY embedding_profile_id ASC LIMIT 1", live.Authority.profile.ProfileID).Scan(&persisted).Error; err != nil {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, err
	}
	providerRef := "sha256:" + uciInstalledAcceptanceStringDigest(provider.URL)
	if persisted.ProviderRef != providerRef || persisted.Model != provider.Model {
		return uci.UCISLOEnvironment{}, uci.UCISLOProfile{}, errors.New("installed watcher embedding readiness did not use the caller-configured provider")
	}
	return uci.UCISLOEnvironment{
			Host:     uci.UCISLOHost{ID: "installed-watcher-recorder"},
			Database: uci.UCISLODatabase{ID: "postgresql-isolated-schema", Version: databaseVersion, DataSizeBytes: databaseSize},
			Corpus:   uci.UCISLOCorpus{ID: "installed-watcher-bounded-corpus", ManifestDigest: string(finalA.ManifestDigest)},
			Provider: uci.UCISLOProvider{ID: providerRef, Model: provider.Model, Status: "healthy"},
		}, uci.UCISLOProfile{
			ID: live.Authority.profile.ProfileID, TextFileCount: textFiles, LinesOfCode: linesOfCode,
			ActiveWorktreeCount: int(active), InactiveRegistrationCount: int(inactive), LANRTT: 0,
			ChangedFileCount: 1, ChangedBytes: 1,
		}, nil
}

type uciWatcherSLOUnchangedSnapshot struct {
	view            acceptance.UCIWatcherSLOView
	readyCandidates uint64
}

// uciWatcherSLOObserveUnchangedWindow first establishes that the preceding
// changed View is embedding-ready, then captures two independent quiescent
// installed-client snapshots without scheduling another index run.
func uciWatcherSLOObserveUnchangedWindow(
	awaitReady func() (uciInstalledAcceptancePublication, error),
	waitQuiescent func(uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error),
	snapshot func(uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error),
) (uciWatcherSLOUnchangedSnapshot, uciWatcherSLOUnchangedSnapshot, error) {
	ready, err := awaitReady()
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	baseline, err := waitQuiescent(ready)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	before, err := snapshot(baseline)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	afterPublication, err := waitQuiescent(baseline)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	if !uciInstalledAcceptanceSameViewPublication(baseline, afterPublication) {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, errors.New("unchanged watcher observation published a new View")
	}
	after, err := snapshot(afterPublication)
	if err != nil {
		return uciWatcherSLOUnchangedSnapshot{}, uciWatcherSLOUnchangedSnapshot{}, err
	}
	return before, after, nil
}

func uciRecordInstalledWatcherSLOUnchanged(ctx context.Context, live uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection, before uciInstalledAcceptancePublication, identity uci.UCISLOSampleIdentity) (acceptance.UCIWatcherSLOUnchangedInputCounter, error) {
	baselineBefore, baselineAfter, err := uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) {
			_, ready, _, readyErr := uciWatcherSLOAwaitEmbeddingReady(ctx, live.Authority, live.ClientA, selection, before)
			return ready, readyErr
		},
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			return uciWaitForInstalledAcceptanceRestartQuiescence(ctx, live.ClientA, selection, expected)
		},
		func(expected uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			view, status, snapshotErr := uciWatcherSLOCurrentView(ctx, live.Authority, live.ClientA, selection, expected)
			if snapshotErr != nil {
				return uciWatcherSLOUnchangedSnapshot{}, snapshotErr
			}
			if !uciWatcherSLOEmbeddingReady(status) {
				return uciWatcherSLOUnchangedSnapshot{}, errors.New("unchanged watcher baseline is not embedding-ready")
			}
			return uciWatcherSLOUnchangedSnapshot{view: view, readyCandidates: status.Embedding.ReadyCandidates}, nil
		},
	)
	if err != nil {
		return acceptance.UCIWatcherSLOUnchangedInputCounter{}, err
	}
	if !uciWatcherSLOViewsEqual(baselineBefore.view, baselineAfter.view) || baselineBefore.readyCandidates != baselineAfter.readyCandidates {
		return acceptance.UCIWatcherSLOUnchangedInputCounter{}, errors.New("unchanged installed input changed its View or embedding-ready counter")
	}
	return acceptance.UCIWatcherSLOUnchangedInputCounter{
		Identity: identity, Context: baselineAfter.view.Context, InputDigest: string(baselineAfter.view.ManifestDigest), Unchanged: true,
		EmbeddingCountersOrigin: acceptance.UCIWatcherSLOOriginInstalledEmbeddingStatus, EmbeddingCountersMeasured: true,
		EmbeddingCountersBefore: int64(baselineBefore.readyCandidates), EmbeddingCountersAfter: int64(baselineAfter.readyCandidates),
		ReembeddedCandidates: 0,
	}, nil
}

func uciWatcherSLOMarkAuxiliaryInactive(ctx context.Context, authority *uciInstalledAcceptanceAuthority) error {
	checkout := authority.checkouts["auxiliary-4"]
	if checkout == nil {
		return errors.New("installed watcher recorder auxiliary checkout is unavailable")
	}
	result := authority.store.GetDB().WithContext(ctx).Model(&gormdb.UCICheckout{}).Where("checkout_id = ? AND state = ?", checkout.CheckoutID, gormdb.UCICheckoutRegistered).Update("state", gormdb.UCICheckoutUnregistered)
	if result.Error != nil || result.RowsAffected != 1 {
		return errors.New("mark installed watcher auxiliary checkout inactive")
	}
	return nil
}

func uciWatcherSLOCandidate(ctx context.Context, root string, candidates map[string]uciInstallHarnessCommand) (uci.UCISLOCandidate, error) {
	branch, err := uciReadInstalledAcceptanceGit(ctx, root, "branch", "--show-current")
	if err != nil || branch == "" {
		return uci.UCISLOCandidate{}, errors.New("resolve installed watcher candidate branch")
	}
	commit, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	tree, err := uciReadInstalledAcceptanceGit(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return uci.UCISLOCandidate{}, err
	}
	artifacts := []struct {
		role string
		name string
	}{{"daemon", "engram-daemon"}, {"server", "engram-server"}, {"parser", "uci-parser"}}
	digests := make([]uci.UCISLOArtifactDigest, 0, len(artifacts))
	for _, artifact := range artifacts {
		candidate, found := candidates[artifact.role]
		if !found {
			return uci.UCISLOCandidate{}, errors.New("installed watcher candidate artifact is unavailable")
		}
		digest, err := uciInstalledAcceptanceFileSHA256(candidate.Executable)
		if err != nil {
			return uci.UCISLOCandidate{}, err
		}
		digests = append(digests, uci.UCISLOArtifactDigest{Name: artifact.name, Digest: "sha256:" + digest})
	}
	return uci.UCISLOCandidate{Branch: branch, Commit: commit, Tree: tree, ArtifactDigests: digests}, nil
}

func uciWatcherSLOCorpusCounts(root string) (int, int, error) {
	files, lines := 0, 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		lines += strings.Count(string(contents), "\n")
		return nil
	})
	if err != nil || files == 0 || lines == 0 {
		return 0, 0, errors.New("measure installed watcher bounded corpus")
	}
	return files, lines, nil
}

func uciWatcherSLOChangedBytes(previous, next []byte) int64 {
	limit := len(previous)
	if len(next) < limit {
		limit = len(next)
	}
	changed := 0
	for index := range limit {
		if previous[index] != next[index] {
			changed++
		}
	}
	return int64(changed + len(previous) - limit + len(next) - limit)
}

func uciWatcherSLOGitClean(ctx context.Context, root string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "status", "--porcelain=v1")
	command.Dir = root
	command.Stderr = io.Discard
	output, err := command.Output()
	if err != nil {
		return false, fmt.Errorf("inspect installed watcher candidate Git status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) == 0, nil
}

func uciWatcherSLOInstalledTiming(origin acceptance.UCIWatcherSLOOrigin, started, completed time.Time) acceptance.UCIWatcherSLOTiming {
	return acceptance.UCIWatcherSLOTiming{Origin: origin, Measured: true, StartedAt: started, CompletedAt: completed, Latency: completed.Sub(started)}
}

func uciWatcherSLOViewsEqual(left, right acceptance.UCIWatcherSLOView) bool {
	return left.BuildID == right.BuildID && left.Context == right.Context && left.ManifestDigest == right.ManifestDigest && left.AcceptedFSSeq == right.AcceptedFSSeq && left.PublishedAt.Equal(right.PublishedAt)
}

func uciWatcherSLOClassifyOutcome(status uci.QueryResponseStatus, structuralCoverage, embeddingCoverage string) (string, string) {
	if status == uci.QueryStatusOK && structuralCoverage == "complete" && embeddingCoverage == "complete" {
		return "healthy", ""
	}
	return "degraded", fmt.Sprintf("installed watcher structural readiness is %s/%s while embedding readiness is %s", status, structuralCoverage, embeddingCoverage)
}

func uciWriteInstalledWatcherSLORecord(path string, encoded []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uci-installed-watcher-slo-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func uciWatcherSLOTestStatusPayload(t *testing.T, publication uciInstalledAcceptancePublication, embedding uciWatcherSLOStageEmbedding) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": "idle",
		"run_id": publication.runID,
		"context": map[string]any{
			"source_id": publication.sourceID, "checkout_id": publication.checkoutID, "view_id": publication.viewID, "profile_id": publication.profileID, "generation": publication.generation,
		},
		"freshness": map[string]any{"state": "observed_current", "pending_changes": 0},
		"embedding": map[string]any{
			"coverage": embedding.Coverage, "total_candidates": embedding.TotalCandidates, "ready_candidates": embedding.ReadyCandidates, "pending_jobs": embedding.PendingJobs, "job_state": embedding.JobState, "error_code": embedding.ErrorCode, "retry_after": embedding.RetryAfterUTC,
		},
	})
	if err != nil {
		t.Fatalf("encode watcher status payload: %v", err)
	}
	return payload
}

func uciWatcherSLOEvent(events []uciWatcherSLOStageEvent, name string) *uciWatcherSLOStageEvent {
	for index := range events {
		if events[index].Name == name {
			return &events[index]
		}
	}
	return nil
}

func TestUCIWatcherSLOAttemptPersistsTerminalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watcher-slo.json")
	journal, err := uciNewWatcherSLOStageJournal(path)
	if err != nil {
		t.Fatalf("create stage journal: %v", err)
	}
	baseline := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	after := baseline
	after.viewID, after.generation, after.runID = "view-after", 2, "run-after"
	attempt := uciWatcherSLOStageAttempt{ID: "attempt-001", Sequence: 1, Warmth: "cold", Baseline: uciWatcherSLOStagePublicationFor(baseline)}
	if err := journal.append("run_begin", &uciWatcherSLOStageRun{}, nil); err != nil {
		t.Fatalf("write run begin: %v", err)
	}
	if err := journal.append("attempt_begin", nil, &attempt); err != nil {
		t.Fatalf("write attempt begin: %v", err)
	}
	trace := newUCIWatcherSLOAttemptTrace(journal.origin, baseline)
	saveStarted := journal.origin.Add(time.Millisecond)
	trace.markSave(saveStarted, saveStarted.Add(time.Millisecond), nil)
	trace.setPublication(after)
	trace.observeTool("codebase_status", saveStarted.Add(2*time.Millisecond), saveStarted.Add(3*time.Millisecond), uciWatcherSLOTestStatusPayload(t, after, uciWatcherSLOStageEmbedding{Coverage: "unavailable", TotalCandidates: 1, PendingJobs: 1, JobState: "failed_terminal", ErrorCode: "provider_unavailable"}), nil)
	terminalErr := errors.New("terminal embedding failure")
	if err := uciWatcherSLOFinalizeAttempt(journal, &attempt, trace, "embedding", terminalErr, uciWatcherSLOStageEmbedding{JobState: "failed_terminal", ErrorCode: "provider_unavailable"}, acceptance.UCIWatcherSLOBatch{}); !errors.Is(err, terminalErr) {
		t.Fatalf("finalize terminal attempt error = %v", err)
	}
	if err := journal.append("run_end", &uciWatcherSLOStageRun{Status: "failed", FailureClass: "embedding_terminal_failure"}, nil); err != nil {
		t.Fatalf("write run end: %v", err)
	}
	if err := journal.close(); err != nil {
		t.Fatalf("close stage journal: %v", err)
	}
	raw, err := os.ReadFile(path + ".attempts.jsonl")
	if err != nil {
		t.Fatalf("read stage journal: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 4 {
		t.Fatalf("journal record count = %d, want 4", len(lines))
	}
	var terminal uciWatcherSLOStageJournalRecord
	if err := json.Unmarshal([]byte(lines[2]), &terminal); err != nil {
		t.Fatalf("decode terminal attempt: %v", err)
	}
	if terminal.SchemaVersion != uciWatcherSLOStageDiagnosticsSchemaVersion || terminal.Record != "attempt_end" || terminal.Attempt == nil || terminal.Attempt.Status != "failed" || terminal.Attempt.FailureClass != "embedding_terminal_failure" || terminal.Attempt.CompleteV2Batch {
		t.Fatalf("terminal journal record = %#v", terminal)
	}
	if uciWatcherSLOEvent(terminal.Attempt.Events, "save_begin") == nil || uciWatcherSLOEvent(terminal.Attempt.Events, "embedding_status_observed") == nil || uciWatcherSLOEvent(terminal.Attempt.Events, "embedding_ready_first_seen") != nil {
		t.Fatalf("terminal milestones = %#v", terminal.Attempt.Events)
	}
}

func TestUCIWatcherSLODurableViewScanObservationKeepsClockDomainsSeparate(t *testing.T) {
	localOrigin := time.Date(2042, time.January, 2, 3, 4, 5, 0, time.UTC)
	dbScanStart := time.Date(2026, time.September, 9, 12, 0, 0, 123, time.FixedZone("db", -7*60*60))
	dbScanEnd := dbScanStart.Add(23*time.Millisecond + 7*time.Nanosecond)
	stage, err := uciWatcherSLOStagePublicationForDurableView(uciWatcherSLODurableViewRow{ViewID: "view-after", Generation: 2, ScanStart: &dbScanStart, ScanEnd: &dbScanEnd})
	if err != nil {
		t.Fatalf("stage durable View scan window: %v", err)
	}
	if stage.ScanStartDBUTC == nil || !stage.ScanStartDBUTC.Equal(dbScanStart.UTC()) || stage.ScanEndDBUTC == nil || !stage.ScanEndDBUTC.Equal(dbScanEnd.UTC()) || stage.ScanDurationNS == nil || *stage.ScanDurationNS != dbScanEnd.Sub(dbScanStart).Nanoseconds() {
		t.Fatalf("durable View DB-clock observation = %#v", stage)
	}

	baseline := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1}
	after := baseline
	after.viewID, after.generation = "view-after", 2
	trace := newUCIWatcherSLOAttemptTrace(localOrigin, baseline)
	trace.markSave(localOrigin, localOrigin, nil)
	trace.setPublication(after)
	trace.observeDurable(localOrigin.Add(5*time.Millisecond), localOrigin.Add(7*time.Millisecond), after, stage, true, nil)
	journalPath := filepath.Join(t.TempDir(), "watcher-slo.json")
	journal, err := uciNewWatcherSLOStageJournal(journalPath)
	if err != nil {
		t.Fatalf("create stage journal: %v", err)
	}
	attempt := uciWatcherSLOStageAttempt{ID: "attempt-001", Sequence: 1, Warmth: "warm", Baseline: uciWatcherSLOStagePublicationFor(baseline)}
	if err := uciWatcherSLOFinalizeAttempt(journal, &attempt, trace, "", nil, uciWatcherSLOStageEmbedding{}, acceptance.UCIWatcherSLOBatch{}); err != nil {
		t.Fatalf("persist scan observation: %v", err)
	}
	if err := journal.close(); err != nil {
		t.Fatalf("close stage journal: %v", err)
	}
	raw, err := os.ReadFile(journalPath + ".attempts.jsonl")
	if err != nil {
		t.Fatalf("read stage journal: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode stage journal: %v", err)
	}
	attemptRecord, ok := record["attempt"].(map[string]any)
	if !ok {
		t.Fatalf("journal attempt = %#v", record["attempt"])
	}
	events, ok := attemptRecord["events"].([]any)
	if !ok || len(events) != 3 {
		t.Fatalf("journal events = %#v", attemptRecord["events"])
	}
	durable, ok := events[2].(map[string]any)
	if !ok || durable["name"] != "durable_view_first_seen" {
		t.Fatalf("durable journal event = %#v", events[2])
	}
	span, ok := durable["span"].(map[string]any)
	if !ok || int64(span["elapsed_ns"].(float64)) != (2*time.Millisecond).Nanoseconds() {
		t.Fatalf("local monotonic span = %#v", durable["span"])
	}
	publication, ok := durable["publication"].(map[string]any)
	if !ok || publication["scan_start_db_utc"] != dbScanStart.UTC().Format(time.RFC3339Nano) || publication["scan_end_db_utc"] != dbScanEnd.UTC().Format(time.RFC3339Nano) || int64(publication["scan_duration_ns"].(float64)) != dbScanEnd.Sub(dbScanStart).Nanoseconds() {
		t.Fatalf("durable DB-clock fields = %#v", durable["publication"])
	}

	unknown, err := uciWatcherSLOStagePublicationForDurableView(uciWatcherSLODurableViewRow{})
	if err != nil {
		t.Fatalf("stage missing scan window: %v", err)
	}
	encodedUnknown, err := json.Marshal(unknown)
	if err != nil {
		t.Fatalf("encode missing scan window: %v", err)
	}
	var unknownFields map[string]any
	if err := json.Unmarshal(encodedUnknown, &unknownFields); err != nil {
		t.Fatalf("decode missing scan window: %v", err)
	}
	for _, field := range []string{"scan_start_db_utc", "scan_end_db_utc", "scan_duration_ns"} {
		value, present := unknownFields[field]
		if !present || value != nil {
			t.Fatalf("missing scan field %q = %#v, present %t", field, value, present)
		}
	}
	zero := time.Time{}
	if _, err := uciWatcherSLOStagePublicationForDurableView(uciWatcherSLODurableViewRow{ScanStart: &zero}); err == nil {
		t.Fatal("zero scan_start was accepted")
	}
	if _, err := uciWatcherSLOStagePublicationForDurableView(uciWatcherSLODurableViewRow{ScanStart: &dbScanEnd, ScanEnd: &dbScanStart}); err == nil {
		t.Fatal("inverted scan window was accepted")
	}
}

func TestUCIWatcherSLOPublicationObservationBounds(t *testing.T) {
	save := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	lower, upper, ok := uciWatcherSLOPublicationObservationBounds(save, save.Add(15*time.Millisecond), save.Add(90*time.Millisecond), save.Add(25*time.Millisecond))
	if !ok || lower != 15*time.Millisecond || upper != 25*time.Millisecond {
		t.Fatalf("publication bounds = [%s,%s], %t; want [15ms,25ms], true", lower, upper, ok)
	}
	baseline := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "before", generation: 1}
	selected := baseline
	selected.viewID, selected.generation = "selected", 2
	foreign := selected
	foreign.viewID, foreign.generation = "foreign", 3
	trace := newUCIWatcherSLOAttemptTrace(save, baseline)
	trace.markSave(save, save, nil)
	trace.observeDurable(save.Add(time.Millisecond), save.Add(2*time.Millisecond), foreign, uciWatcherSLOStagePublication{ViewID: foreign.viewID, Generation: foreign.generation, PublishedAtUTC: func() *time.Time { skewed := save.Add(-24 * time.Hour); return &skewed }()}, true, nil)
	trace.setPublication(selected)
	if uciWatcherSLOEvent(trace.snapshot(), "durable_view_first_seen") != nil {
		t.Fatal("foreign durable View became the selected publication")
	}
}

func TestUCIWatcherSLOAwaitSearchableUsesOneCanarySearch(t *testing.T) {
	before := uciInstalledAcceptancePublication{
		sourceID: "20000000-0000-4000-8000-000000000001", checkoutID: "30000000-0000-4000-8000-000000000001", profileID: "50000000-0000-4000-8000-000000000001", viewID: "40000000-0000-4000-8000-000000000001", generation: 1, runID: "run-before",
	}
	after := before
	after.viewID, after.generation, after.runID = "40000000-0000-4000-8000-000000000002", 2, "run-after"
	statusPayload, err := json.Marshal(map[string]any{
		"status": "idle", "run_id": after.runID,
		"context":   map[string]any{"source_id": after.sourceID, "checkout_id": after.checkoutID, "view_id": after.viewID, "profile_id": after.profileID, "generation": after.generation},
		"freshness": map[string]any{"state": "observed_current", "pending_changes": 0, "barrier": map[string]any{"scope": map[string]any{"kind": "paths", "path_count": 1}, "deadline_ms": 1, "state": "satisfied"}},
	})
	if err != nil {
		t.Fatalf("encode watcher status: %v", err)
	}
	zero := int64(0)
	contexts := uci.QueryContexts{{SourceID: after.sourceID, CheckoutID: after.checkoutID, ViewID: after.viewID, Generation: after.generation, ProfileID: after.profileID}}
	items := uci.QueryItems{{
		Ref:           uci.QueryEntityRef{SourceID: after.sourceID, ViewID: after.viewID, EntityKey: "go:pkg/watcher.go/func:UCIWatcherCanary"},
		Path:          "pkg/watcher.go",
		Span:          uci.QuerySpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
		ContentDigest: uci.QueryContentDigest(strings.Repeat("a", 64)),
		Kind:          uci.QueryItemCode,
		Language:      "go",
		Excerpt:       "func UCIWatcherCanary() {}",
		MatchSources:  []uci.QueryMatchSource{uci.QueryMatchExact},
	}}
	warnings := uci.QueryWarnings{}
	truncated := false
	query := uci.QueryResponse{
		Schema:       uci.QueryResponseSchema,
		Status:       uci.QueryStatusOK,
		Contexts:     &contexts,
		Freshness:    &uci.QueryFreshness{State: uci.QueryFreshnessObservedCurrent, Method: uci.QueryFreshnessWatchWatermark, PendingChanges: &zero, EnrichmentWatermark: uci.QueryEnrichmentWatermark{Sequence: after.generation, State: uci.QueryEnrichmentCurrent}},
		Retrieval:    &uci.QueryRetrieval{Mode: uci.QueryRetrievalExact, DegradationReasons: []string{}},
		Coverage:     &uci.QueryCoverage{Structural: uci.IndexCoverageComplete, UnresolvedSites: &zero, UnsupportedFiles: &zero},
		Exposure:     &uci.QueryExposure{ExposureRef: uci.NewExposureRef(), CompletionState: uci.QueryCompletionUnknown},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &uci.QueryContinuation{},
	}
	if err := query.Validate(); err != nil {
		t.Fatalf("build validated canary response: %v", err)
	}
	queryPayload, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("encode canary response: %v", err)
	}

	requests, clientInput, err := os.Pipe()
	if err != nil {
		t.Fatalf("open client input pipe: %v", err)
	}
	responses, serverOutput, err := os.Pipe()
	if err != nil {
		_ = requests.Close()
		_ = clientInput.Close()
		t.Fatalf("open server output pipe: %v", err)
	}
	client := &uciInstalledAcceptanceMCPClient{name: "watcher-test", writer: bufio.NewWriter(clientInput), scanner: bufio.NewScanner(responses)}
	client.scanner.Buffer(make([]byte, 64*1024), 16<<20)
	serverErr := make(chan error, 1)
	serverDone := make(chan struct{})
	searchCalls := make(chan int, 1)
	statusCalls := make(chan int, 1)
	toolCalls := make(chan []string, 1)
	go func() {
		defer close(serverDone)
		defer func() { _ = serverOutput.Close() }()
		calls, statuses := 0, 0
		tools := make([]string, 0, 2+uciInstalledAcceptanceQuiescenceObservations)
		defer func() { searchCalls <- calls; statusCalls <- statuses; toolCalls <- tools }()
		scanner := bufio.NewScanner(requests)
		writer := json.NewEncoder(serverOutput)
		for scanner.Scan() {
			var request struct {
				ID     string `json:"id"`
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				serverErr <- fmt.Errorf("decode watcher request: %w", err)
				return
			}
			if request.Method != "tools/call" {
				serverErr <- fmt.Errorf("watcher request method = %q, want tools/call", request.Method)
				return
			}
			tools = append(tools, request.Params.Name)
			payload := statusPayload
			switch request.Params.Name {
			case "codebase_status":
				statuses++
			case "codebase_search":
				calls++
				payload = queryPayload
			default:
				serverErr <- fmt.Errorf("watcher tool = %q", request.Params.Name)
				return
			}
			if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}, "isError": false}}); err != nil {
				serverErr <- fmt.Errorf("write watcher response: %w", err)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			serverErr <- fmt.Errorf("read watcher request: %w", err)
		}
	}()
	closed := false
	closeServer := func() {
		if closed {
			return
		}
		closed = true
		_ = clientInput.Close()
		<-serverDone
		_ = requests.Close()
		_ = responses.Close()
	}
	defer closeServer()

	trace := newUCIWatcherSLOAttemptTrace(time.Now(), before)
	restoreObserver := client.setToolObserver(trace.observeTool)
	publication, response, structuralCompleted, err := uciWatcherSLOAwaitSearchable(context.Background(), client, uciInstalledAcceptanceSelection{contextHandle: "context", runID: before.runID}, before, "UCIWatcherCanary", "pkg/watcher.go", trace)
	if err != nil {
		t.Fatalf("await searchable watcher: %v", err)
	}
	barrier, err := uciWatcherSLOAwaitPostEndpointBarrier(context.Background(), client, uciInstalledAcceptanceSelection{contextHandle: "context"}, publication, trace)
	if err != nil {
		t.Fatalf("await post-endpoint barrier: %v", err)
	}
	quiescent, err := uciWatcherSLOAwaitPostEndpointQuiescence(context.Background(), client, uciInstalledAcceptanceSelection{contextHandle: "context"}, publication, barrier, trace)
	if err != nil {
		t.Fatalf("await post-endpoint quiescence: %v", err)
	}
	if !uciWatcherSLOSameExactPublication(publication, barrier) || !uciWatcherSLOSameExactPublication(publication, quiescent) {
		t.Fatalf("post-endpoint safety changed publication: endpoint=%#v barrier=%#v quiescent=%#v", publication, barrier, quiescent)
	}
	restoreObserver()
	closeServer()
	select {
	case err := <-serverErr:
		t.Fatal(err)
	default:
	}
	if calls := <-searchCalls; calls != 1 {
		t.Fatalf("codebase_search calls after one publication = %d, want 1", calls)
	}
	if calls := <-statusCalls; calls != 2+uciInstalledAcceptanceQuiescenceObservations {
		t.Fatalf("codebase_status calls through post-endpoint safety = %d, want %d", calls, 2+uciInstalledAcceptanceQuiescenceObservations)
	}
	if tools := <-toolCalls; len(tools) < 3 || tools[0] != "codebase_status" || tools[1] != "codebase_search" || tools[2] != "codebase_status" {
		t.Fatalf("tool ordering = %#v, want first View then canary then post-endpoint safety", tools)
	}
	if publication.sourceID != after.sourceID || publication.checkoutID != after.checkoutID || publication.profileID != after.profileID || publication.viewID != after.viewID || publication.generation != after.generation || publication.runID != after.runID || publication.freshnessState != "observed_current" || publication.barrierState != "" || response.Status != uci.QueryStatusOK || !uciInstalledAcceptanceQueryMatchesPublication(response, publication) {
		t.Fatalf("retained canary response = %#v for publication %#v", response, publication)
	}
	events := trace.snapshot()
	searchReturns := 0
	for _, event := range events {
		if event.Name == "search_return" {
			searchReturns++
		}
	}
	canary := uciWatcherSLOEvent(events, "canary_search_return")
	barrierEvent := uciWatcherSLOEvent(events, "post_endpoint_barrier_begin")
	quiescence := uciWatcherSLOEvent(events, "post_endpoint_quiescence_begin")
	if searchReturns != 1 || uciWatcherSLOEvent(events, "client_view_first_seen") == nil || canary == nil || barrierEvent == nil || quiescence == nil || canary.Span.ReturnedElapsedNS > barrierEvent.Span.StartedElapsedNS || barrierEvent.Span.ReturnedElapsedNS > quiescence.Span.StartedElapsedNS || structuralCompleted.After(trace.origin.Add(time.Duration(barrierEvent.Span.StartedElapsedNS))) || uciWatcherSLOEvent(events, "final_search_begin") != nil || uciWatcherSLOEvent(events, "final_search_return") != nil {
		t.Fatalf("endpoint and safety diagnostics = %#v", events)
	}
}

func TestUCIInstalledWatcherFirstCurrentPublicationUsesNewRunForSubsequentStatus(t *testing.T) {
	before := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	after := before
	after.viewID, after.generation, after.runID = "view-after", 2, "run-after"
	first, err := uciDecodeInstalledAcceptanceStatus(uciWatcherSLOTestStatusPayload(t, before, uciWatcherSLOStageEmbedding{}))
	if err != nil {
		t.Fatalf("decode first watcher status: %v", err)
	}
	first.runID = after.runID
	current, err := uciDecodeInstalledAcceptanceStatus(uciWatcherSLOTestStatusPayload(t, after, uciWatcherSLOStageEmbedding{}))
	if err != nil {
		t.Fatalf("decode current watcher status: %v", err)
	}

	calls := 0
	publication, err := uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(context.Background(), uciInstalledAcceptanceSelection{contextHandle: "checkout-handle", runID: before.runID}, before, func(_ context.Context, selection uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
		calls++
		switch calls {
		case 1:
			if selection.contextHandle != "checkout-handle" || selection.runID != before.runID {
				return uciInstalledAcceptanceStatus{}, fmt.Errorf("initial watcher status selection = %#v", selection)
			}
			return first, nil
		case 2:
			if selection.contextHandle != "checkout-handle" || selection.runID != after.runID {
				return uciInstalledAcceptanceStatus{}, fmt.Errorf("stale watcher run pairing = %#v, want run_id %q", selection, after.runID)
			}
			return current, nil
		default:
			return uciInstalledAcceptanceStatus{}, fmt.Errorf("watcher status calls = %d, want 2", calls)
		}
	})
	if err != nil {
		t.Fatalf("await first current watcher publication: %v", err)
	}
	if calls != 2 || !uciWatcherSLOSameExactPublication(publication, after) {
		t.Fatalf("first current watcher publication = %#v after %d calls, want %#v after 2", publication, calls, after)
	}
}

func TestUCIInstalledWatcherFirstCurrentPublicationRetriesTransientContextMismatch(t *testing.T) {
	before := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	after := before
	after.viewID, after.generation, after.runID = "view-after", 2, "run-after"
	current, err := uciDecodeInstalledAcceptanceStatus(uciWatcherSLOTestStatusPayload(t, after, uciWatcherSLOStageEmbedding{}))
	if err != nil {
		t.Fatalf("decode current watcher status: %v", err)
	}

	selection := uciInstalledAcceptanceSelection{contextHandle: "checkout-handle", runID: before.runID}
	calls := 0
	publication, err := uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(context.Background(), selection, before, func(_ context.Context, observed uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
		calls++
		if observed != selection {
			return uciInstalledAcceptanceStatus{}, fmt.Errorf("transient watcher retry changed selection = %#v, want %#v", observed, selection)
		}
		if calls == 1 {
			return uciInstalledAcceptanceStatus{}, &uciInstalledAcceptanceMCPError{method: "tools/call", code: "-32603", detail: "CONTEXT_MISMATCH"}
		}
		if calls == 2 {
			return current, nil
		}
		return uciInstalledAcceptanceStatus{}, fmt.Errorf("watcher status calls = %d, want 2", calls)
	})
	if err != nil {
		t.Fatalf("retry transient context mismatch: %v", err)
	}
	if calls != 2 || !uciWatcherSLOSameExactPublication(publication, after) {
		t.Fatalf("first current watcher publication = %#v after %d calls, want %#v after 2", publication, calls, after)
	}
}

func TestUCIInstalledWatcherFirstCurrentPublicationRetriesTransientContextMismatchUntilDeadline(t *testing.T) {
	before := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	calls := 0
	_, err := uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(ctx, uciInstalledAcceptanceSelection{contextHandle: "checkout-handle", runID: before.runID}, before, func(context.Context, uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
		calls++
		return uciInstalledAcceptanceStatus{}, &uciInstalledAcceptanceMCPError{method: "tools/call", code: "-32603", detail: "CONTEXT_MISMATCH"}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transient context mismatch deadline error = %v, want deadline exceeded", err)
	}
	if calls < 2 {
		t.Fatalf("transient context mismatch status calls = %d, want repeated observation", calls)
	}
}

func TestUCIInstalledWatcherFirstCurrentPublicationRejectsNonContextError(t *testing.T) {
	before := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	want := &uciInstalledAcceptanceMCPError{method: "tools/call", code: "-32603", detail: "PERMISSION_DENIED"}
	calls := 0
	_, err := uciWaitForInstalledAcceptanceWatcherFirstCurrentPublicationObserved(context.Background(), uciInstalledAcceptanceSelection{contextHandle: "checkout-handle", runID: before.runID}, before, func(context.Context, uciInstalledAcceptanceSelection) (uciInstalledAcceptanceStatus, error) {
		calls++
		return uciInstalledAcceptanceStatus{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("non-context watcher error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("non-context watcher status calls = %d, want 1", calls)
	}
}

func TestUCIWatcherSLOObserverKeepsFirstExactViewObservation(t *testing.T) {
	origin := time.Date(2026, time.September, 9, 1, 0, 0, 0, time.UTC)
	baseline := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "before", generation: 1, runID: "run-before"}
	after := baseline
	after.viewID, after.generation, after.runID = "after", 2, "run-after"
	trace := newUCIWatcherSLOAttemptTrace(origin, baseline)
	trace.observeTool("codebase_status", origin, origin.Add(time.Millisecond), uciWatcherSLOTestStatusPayload(t, baseline, uciWatcherSLOStageEmbedding{Coverage: "partial", JobState: "queued"}), nil)
	trace.observeTool("codebase_status", origin.Add(2*time.Millisecond), origin.Add(3*time.Millisecond), json.RawMessage(`{"status":"running","run_id":"run-after"}`), nil)
	exactReturned := origin.Add(5 * time.Millisecond)
	ready := uciWatcherSLOStageEmbedding{Coverage: "complete", TotalCandidates: 2, ReadyCandidates: 2, JobState: "succeeded"}
	trace.observeTool("codebase_status", origin.Add(4*time.Millisecond), exactReturned, uciWatcherSLOTestStatusPayload(t, after, ready), nil)
	trace.setPublication(after)
	trace.observeCall("canary_search", origin.Add(6*time.Millisecond), origin.Add(7*time.Millisecond), nil)
	trace.observeStage("post_endpoint_barrier", origin.Add(8*time.Millisecond), origin.Add(9*time.Millisecond), nil)
	trace.observeStage("post_endpoint_quiescence", origin.Add(10*time.Millisecond), origin.Add(11*time.Millisecond), nil)
	trace.observeTool("codebase_status", origin.Add(12*time.Millisecond), origin.Add(13*time.Millisecond), uciWatcherSLOTestStatusPayload(t, after, ready), nil)
	events := trace.snapshot()
	first := uciWatcherSLOEvent(events, "client_view_first_seen")
	if first == nil || first.Span.ReturnedElapsedNS != exactReturned.Sub(origin).Nanoseconds() {
		t.Fatalf("first exact client View = %#v", first)
	}
	for _, name := range []string{"canary_search_begin", "canary_search_return", "post_endpoint_barrier_begin", "post_endpoint_barrier_return", "post_endpoint_quiescence_begin", "post_endpoint_quiescence_return", "embedding_ready_first_seen"} {
		if uciWatcherSLOEvent(events, name) == nil {
			t.Fatalf("missing %s in %#v", name, events)
		}
	}
	if uciWatcherSLOEvent(events, "final_search_begin") != nil || uciWatcherSLOEvent(events, "final_search_return") != nil {
		t.Fatalf("fabricated final search diagnostics = %#v", events)
	}
	client := &uciInstalledAcceptanceMCPClient{}
	calls := 0
	restore := client.setToolObserver(func(string, time.Time, time.Time, json.RawMessage, error) { calls++ })
	client.observeToolCall("codebase_status", origin, origin, nil, nil)
	restore()
	client.observeToolCall("codebase_status", origin, origin, nil, nil)
	if calls != 1 {
		t.Fatalf("scoped passive observer calls = %d, want 1", calls)
	}
}

func TestUCIWatcherSLOScannerAggregateObserverJoinsExactAttemptWithoutPathsOrBodies(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "watcher-slo.json")
	journal, err := uciNewWatcherSLOStageJournal(journalPath)
	if err != nil {
		t.Fatalf("create scanner aggregate journal: %v", err)
	}
	baseline := uciInstalledAcceptancePublication{sourceID: "source-id", checkoutID: "checkout-id", profileID: "profile-id", viewID: "before", generation: 1}
	attempt := uciWatcherSLOStageAttempt{ID: "attempt-001", Sequence: 1, Warmth: "warm", Baseline: uciWatcherSLOStagePublicationFor(baseline)}
	trace := newUCIWatcherSLOAttemptTrace(journal.origin, baseline)
	observer := uciNewWatcherSLOScannerAggregateObserver()
	defer observer.close()
	observer.setTrace(trace)
	started := time.Date(2026, time.September, 9, 12, 0, 0, 123, time.UTC)
	wanted := uciWatcherSLOScannerAggregate{
		SourceID: "source-id", CheckoutID: "checkout-id", ProfileID: "profile-id", ObservedFSSeq: 41,
		ScanStartedAt: started, ScanCompletedAt: started.Add(23 * time.Millisecond),
		GitTopologyDurationNS: 11, GitStatusDurationNS: 13, GitStagedDurationNS: 17, GitUntrackedDurationNS: 19,
		CandidateLoopDurationNS: 23, ScanTotalDurationNS: 101, ResidualDurationNS: 18,
		CandidateCount: 29, AdmittedCount: 31, ExcludedCount: 37, UnreadableCount: 41, BytesRead: 43,
	}
	post := func(payload any) int {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode scanner aggregate: %v", err)
		}
		response, err := http.Post(observer.endpoint(), "application/json", bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("post scanner aggregate: %v", err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := post(wanted); status != http.StatusNoContent {
		t.Fatalf("scanner aggregate status = %d, want %d", status, http.StatusNoContent)
	}
	mismatched := wanted
	mismatched.ProfileID = "other-profile"
	if status := post(mismatched); status != http.StatusNoContent {
		t.Fatalf("mismatched scanner aggregate status = %d, want %d", status, http.StatusNoContent)
	}
	if status := post(map[string]any{
		"source_id": wanted.SourceID, "checkout_id": wanted.CheckoutID, "profile_id": wanted.ProfileID, "observed_fs_seq": wanted.ObservedFSSeq,
		"scan_started_at": wanted.ScanStartedAt, "scan_completed_at": wanted.ScanCompletedAt,
		"root_path": `C:\\sensitive\\candidate`, "body": "secret-body-do-not-record",
	}); status != http.StatusBadRequest {
		t.Fatalf("unallowlisted scanner payload status = %d, want %d", status, http.StatusBadRequest)
	}
	after := acceptance.UCIWatcherSLOView{Context: uci.ContextRef{SourceID: wanted.SourceID, CheckoutID: wanted.CheckoutID, AnalysisProfileID: wanted.ProfileID, ViewID: "after", Generation: 2}, AcceptedFSSeq: wanted.ObservedFSSeq}
	if err := uciWatcherSLOFinalizeAttempt(journal, &attempt, trace, "", nil, uciWatcherSLOStageEmbedding{}, acceptance.UCIWatcherSLOBatch{ID: attempt.ID, Outcome: "healthy", AAfter: after}); err != nil {
		t.Fatalf("finalize scanner aggregate attempt: %v", err)
	}
	if err := journal.close(); err != nil {
		t.Fatalf("close scanner aggregate journal: %v", err)
	}
	raw, err := os.ReadFile(journalPath + ".attempts.jsonl")
	if err != nil {
		t.Fatalf("read scanner aggregate journal: %v", err)
	}
	var record uciWatcherSLOStageJournalRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode scanner aggregate journal: %v", err)
	}
	if record.Record != "attempt_end" || record.Attempt == nil {
		t.Fatalf("scanner aggregate attempt record = %#v", record)
	}
	joined := 0
	for _, event := range record.Attempt.Events {
		if event.Name != "prepared_scanner_phase_aggregate" {
			continue
		}
		joined++
		if event.ScannerAggregate == nil || event.ScannerAggregate.SourceDigest != uciInstalledAcceptanceStringDigest(wanted.SourceID) || event.ScannerAggregate.CheckoutDigest != uciInstalledAcceptanceStringDigest(wanted.CheckoutID) || event.ScannerAggregate.ProfileDigest != uciInstalledAcceptanceStringDigest(wanted.ProfileID) || event.ScannerAggregate.ObservedFSSeq != wanted.ObservedFSSeq || !event.ScannerAggregate.ScanStartedAt.Equal(wanted.ScanStartedAt) || !event.ScannerAggregate.ScanCompletedAt.Equal(wanted.ScanCompletedAt) || event.ScannerAggregate.ScanTotalDurationNS != wanted.ScanTotalDurationNS || event.ScannerAggregate.BytesRead != wanted.BytesRead {
			t.Fatalf("joined scanner aggregate = %#v", event.ScannerAggregate)
		}
	}
	if joined != 1 {
		t.Fatalf("joined scanner aggregate events = %d, want 1", joined)
	}
	for _, forbidden := range []string{"source-id", "checkout-id", "profile-id", `C:\\sensitive\\candidate`, "secret-body-do-not-record"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("scanner aggregate journal retained %q: %s", forbidden, raw)
		}
	}
}

func TestUCIWatcherSLOPostEndpointSafetyRejectsChangedPublication(t *testing.T) {
	expected := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view", generation: 2, runID: "run"}
	changed := expected
	changed.viewID, changed.generation = "changed", 3
	if err := uciWatcherSLORequirePostEndpointPublication(expected, changed); err == nil {
		t.Fatal("post-endpoint safety accepted a changed View")
	}
	changed = expected
	changed.runID = "later-run"
	if err := uciWatcherSLORequirePostEndpointPublication(expected, changed); err == nil {
		t.Fatal("post-endpoint safety accepted a changed run")
	}
}

func TestUCIWatcherSLODiagnosticFailureClasses(t *testing.T) {
	retry := uciWatcherSLOStageEmbedding{JobState: "retry_scheduled", ErrorCode: "provider_unavailable"}
	if got := uciWatcherSLODiagnosticFailureClass("embedding", errors.New("retry"), retry); got != "embedding_retry_observed" {
		t.Fatalf("retry classification = %q", got)
	}
	terminal := uciWatcherSLOStageEmbedding{JobState: "failed_terminal", ErrorCode: "provider_unavailable"}
	if got := uciWatcherSLODiagnosticFailureClass("embedding", errors.New("failed"), terminal); got != "embedding_terminal_failure" {
		t.Fatalf("terminal classification = %q", got)
	}
	if got := uciWatcherSLODiagnosticFailureClass("post_endpoint_quiescence", errors.New("changed"), uciWatcherSLOStageEmbedding{}); got != "publication_failure" {
		t.Fatalf("post-endpoint quiescence classification = %q", got)
	}
	if got := uciWatcherSLODiagnosticMigrationWarningClass(true); got != "setup_migration_warning" {
		t.Fatalf("migration warning classification = %q", got)
	}
	encoded, err := json.Marshal(uciWatcherSLOStageJournalRecord{SchemaVersion: uciWatcherSLOStageDiagnosticsSchemaVersion, Record: "attempt_end", Attempt: &uciWatcherSLOStageAttempt{FailureClass: "embedding_retry_observed", Events: []uciWatcherSLOStageEvent{{Name: "embedding_status_observed", Embedding: &retry}}}})
	if err != nil {
		t.Fatalf("encode diagnostic record: %v", err)
	}
	for _, forbidden := range []string{"context_handle", "postgres://", "https://", "api_key", "SELECT "} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("diagnostic record retained %q: %s", forbidden, encoded)
		}
	}
}

func TestUCIWatcherSLODiagnosticsRecordEachTransientContextMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watcher-slo.json")
	journal, err := uciNewWatcherSLOStageJournal(path)
	if err != nil {
		t.Fatalf("create stage journal: %v", err)
	}
	baseline := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", profileID: "profile", viewID: "view-before", generation: 1, runID: "run-before"}
	attempt := uciWatcherSLOStageAttempt{ID: "attempt-001", Sequence: 1, Warmth: "warm", Baseline: uciWatcherSLOStagePublicationFor(baseline)}
	trace := newUCIWatcherSLOAttemptTrace(journal.origin, baseline)
	mismatch := &uciInstalledAcceptanceMCPError{method: "tools/call", code: "-32603", detail: "CONTEXT_MISMATCH"}
	trace.observeTool("codebase_status", journal.origin.Add(time.Millisecond), journal.origin.Add(2*time.Millisecond), nil, mismatch)
	trace.observeTool("codebase_status", journal.origin.Add(3*time.Millisecond), journal.origin.Add(4*time.Millisecond), nil, mismatch)
	if err := uciWatcherSLOFinalizeAttempt(journal, &attempt, trace, "", nil, uciWatcherSLOStageEmbedding{}, acceptance.UCIWatcherSLOBatch{}); err != nil {
		t.Fatalf("finalize transient context mismatch attempt: %v", err)
	}
	if err := journal.close(); err != nil {
		t.Fatalf("close stage journal: %v", err)
	}
	raw, err := os.ReadFile(path + ".attempts.jsonl")
	if err != nil {
		t.Fatalf("read stage journal: %v", err)
	}
	var record uciWatcherSLOStageJournalRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode stage journal: %v", err)
	}
	if record.Attempt == nil || record.Attempt.Status != "complete" || record.Attempt.FailureClass != "" {
		t.Fatalf("transient context mismatch attempt = %#v", record.Attempt)
	}
	observations := 0
	for _, event := range record.Attempt.Events {
		if event.Name != "codebase_status_return" {
			continue
		}
		observations++
		if event.ErrorClass != "mcp_error" || event.ErrorCode != "CONTEXT_MISMATCH" {
			t.Fatalf("transient context mismatch diagnostic = %#v", event)
		}
	}
	if observations != 2 {
		t.Fatalf("transient context mismatch observations = %d, want 2", observations)
	}
}

func TestUCIInstalledWatcherWaitRejectsStall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	observations := 0
	_, err := uciWaitForInstalledAcceptanceWatcherRun(ctx, "watcher-run-before", func(context.Context) (uciInstalledAcceptanceStatus, error) {
		observations++
		return uciInstalledAcceptanceStatus{runID: "watcher-run-before"}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled watcher wait error = %v, want deadline exceeded", err)
	}
	if observations == 0 {
		t.Fatal("stalled watcher wait made no status observation")
	}
}

func TestUCIWatcherSLOGitCleanAcceptsEmptyPorcelain(t *testing.T) {
	root := t.TempDir()
	if err := exec.CommandContext(t.Context(), "git", "init", "--quiet", root).Run(); err != nil {
		t.Fatalf("initialize clean watcher candidate: %v", err)
	}
	clean, err := uciWatcherSLOGitClean(t.Context(), root)
	if err != nil || !clean {
		t.Fatalf("clean watcher candidate = %t, %v; want true, nil", clean, err)
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty watcher candidate: %v", err)
	}
	clean, err = uciWatcherSLOGitClean(t.Context(), root)
	if err != nil || clean {
		t.Fatalf("dirty watcher candidate = %t, %v; want false, nil", clean, err)
	}
}

func TestUCIInstalledAcceptanceCheckoutShapeRequiresDistinctAB(t *testing.T) {
	checkouts := map[string]*gormdb.UCICheckout{
		uciInstalledAcceptanceClientA: {CheckoutID: "checkout-a"},
		uciInstalledAcceptanceClientB: {CheckoutID: "checkout-b"},
		"auxiliary-1":                 {CheckoutID: "checkout-auxiliary-1"},
		"auxiliary-2":                 {CheckoutID: "checkout-auxiliary-2"},
		"auxiliary-3":                 {CheckoutID: "checkout-auxiliary-3"},
		"auxiliary-4":                 {CheckoutID: "checkout-auxiliary-4"},
	}
	checkoutIDs, err := uciInstalledAcceptanceCheckoutIDs(checkouts)
	if err != nil || len(checkoutIDs) != 6 {
		t.Fatalf("valid recorder checkout shape = %#v, %v", checkoutIDs, err)
	}
	checkouts[uciInstalledAcceptanceClientB] = &gormdb.UCICheckout{CheckoutID: "checkout-a"}
	if _, err := uciInstalledAcceptanceCheckoutIDs(checkouts); err == nil {
		t.Fatal("shared A/B checkout identity was accepted")
	}
	checkouts[uciInstalledAcceptanceClientB] = &gormdb.UCICheckout{CheckoutID: "checkout-b"}
	delete(checkouts, "auxiliary-4")
	if _, err := uciInstalledAcceptanceCheckoutIDs(checkouts); err == nil {
		t.Fatal("incomplete recorder checkout shape was accepted")
	}
}

func TestUCIInstalledAcceptanceScenarioProbePhaseKeepsBaseDefault(t *testing.T) {
	probe := func(context.Context, uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
		return nil, nil
	}
	request := uciInstalledAcceptanceRequest{ScenarioProbe: probe}
	if uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("ordinary scenario probe skipped the base lifecycle")
	}
	request.ScenarioProbePhase = uciInstalledAcceptanceScenarioProbeBeforeBaseWatcher
	if !uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("explicit pre-base scenario probe did not select the recorder lifecycle")
	}
	request.ScenarioProbe = nil
	if uciInstalledAcceptanceScenarioRunsBeforeBaseWatcher(request) {
		t.Fatal("absent scenario probe selected the pre-base lifecycle")
	}
}

func TestUCIWatcherSLOClassifiesPartialSearchAsDegraded(t *testing.T) {
	outcome, reason := uciWatcherSLOClassifyOutcome(uci.QueryStatusPartial, "partial", "complete")
	if outcome != "degraded" || reason == "" {
		t.Fatalf("partial structural result classification = %q, %q", outcome, reason)
	}
	outcome, reason = uciWatcherSLOClassifyOutcome(uci.QueryStatusOK, "complete", "complete")
	if outcome != "healthy" || reason != "" {
		t.Fatalf("complete structural result classification = %q, %q", outcome, reason)
	}
}

func TestUCIWatcherSLOUnchangedWindowWaitsForReadyQuiescentBaseline(t *testing.T) {
	stable := uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", viewID: "view", profileID: "profile", generation: 2}
	step := 0
	before, after, err := uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) {
			if step != 0 {
				t.Fatalf("ready step = %d, want 0", step)
			}
			step++
			return stable, nil
		},
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			if expected != stable || (step != 1 && step != 3) {
				t.Fatalf("quiescence step=%d expected=%#v", step, expected)
			}
			step++
			return stable, nil
		},
		func(expected uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			if expected != stable || (step != 2 && step != 4) {
				t.Fatalf("snapshot step=%d expected=%#v", step, expected)
			}
			step++
			return uciWatcherSLOUnchangedSnapshot{readyCandidates: 7}, nil
		},
	)
	if err != nil || step != 5 || before.readyCandidates != 7 || after.readyCandidates != 7 {
		t.Fatalf("unchanged sequence = before=%#v after=%#v step=%d err=%v", before, after, step, err)
	}

	waits := 0
	_, _, err = uciWatcherSLOObserveUnchangedWindow(
		func() (uciInstalledAcceptancePublication, error) { return stable, nil },
		func(expected uciInstalledAcceptancePublication) (uciInstalledAcceptancePublication, error) {
			waits++
			if expected != stable {
				t.Fatalf("unexpected changed baseline: %#v", expected)
			}
			if waits == 1 {
				return stable, nil
			}
			return uciInstalledAcceptancePublication{sourceID: "source", checkoutID: "checkout", viewID: "new-view", profileID: "profile", generation: 3}, nil
		},
		func(uciInstalledAcceptancePublication) (uciWatcherSLOUnchangedSnapshot, error) {
			return uciWatcherSLOUnchangedSnapshot{}, nil
		},
	)
	if err == nil {
		t.Fatal("unchanged window accepted a watcher-published View")
	}
}

func TestUCIWatcherSLOEmbeddingReadyRequiresSettledWorker(t *testing.T) {
	jobState := "succeeded"
	status := uciRealCorpusEmbeddingStatus{}
	status.Embedding.Coverage = "complete"
	status.Embedding.TotalCandidates = 3
	status.Embedding.ReadyCandidates = 3
	status.Embedding.JobState = &jobState
	if !uciWatcherSLOEmbeddingReady(status) {
		t.Fatal("settled embedding worker was not ready")
	}
	status.Embedding.PendingJobs = 1
	if uciWatcherSLOEmbeddingReady(status) {
		t.Fatal("pending embedding worker was accepted as ready")
	}
}
