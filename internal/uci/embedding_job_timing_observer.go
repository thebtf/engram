package uci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// EnvUCIWatcherSLOEmbeddingTimingObserver is an opt-in loopback endpoint used
// only by the installed watcher SLO recorder. It is absent by default.
const EnvUCIWatcherSLOEmbeddingTimingObserver = "ENGRAM_UCI_WATCHER_SLO_EMBEDDING_TIMING_OBSERVER"

const embeddingJobTimingObserverTimeout = 250 * time.Millisecond

type embeddingJobTimingStage string

const (
	embeddingJobTimingStageClaim     embeddingJobTimingStage = "claim_embedding_job"
	embeddingJobTimingStageAuthorize embeddingJobTimingStage = "authorize"
	embeddingJobTimingStagePrepare   embeddingJobTimingStage = "prepare_embedding_batch"
	embeddingJobTimingStageEmbed     embeddingJobTimingStage = "semantic_embed"
	embeddingJobTimingStageCommit    embeddingJobTimingStage = "commit_embedding_batch"
	embeddingJobTimingStageComplete  embeddingJobTimingStage = "complete_embedding_job"
)

func (stage embeddingJobTimingStage) valid() bool {
	switch stage {
	case embeddingJobTimingStageClaim, embeddingJobTimingStageAuthorize, embeddingJobTimingStagePrepare, embeddingJobTimingStageEmbed, embeddingJobTimingStageCommit, embeddingJobTimingStageComplete:
		return true
	default:
		return false
	}
}

// embeddingJobTimingEvent intentionally contains only correlation and timing
// metadata. It never retains an embedding input, vector, endpoint, credential,
// or raw job identifier.
type embeddingJobTimingEvent struct {
	Stage             embeddingJobTimingStage `json:"stage"`
	JobDigest         string                  `json:"job_digest"`
	ViewID            string                  `json:"view_id"`
	Generation        int64                   `json:"generation"`
	ProfileDigest     string                  `json:"profile_digest"`
	Attempt           int                     `json:"attempt"`
	LeaseEpoch        int64                   `json:"lease_epoch"`
	StartedElapsedNS  int64                   `json:"started_elapsed_ns"`
	ReturnedElapsedNS int64                   `json:"returned_elapsed_ns"`
	ElapsedNS         int64                   `json:"elapsed_ns"`
	CandidateCount    int                     `json:"candidate_count"`
	MissingCount      int                     `json:"missing_count"`
	ResultCode        string                  `json:"result_code"`
}

func (event embeddingJobTimingEvent) valid() bool {
	return event.Stage.valid() && embeddingJobTimingDigestValid(event.JobDigest) && canonicalContextUUID(event.ViewID) &&
		event.Generation > 0 && embeddingJobTimingDigestValid(event.ProfileDigest) && event.Attempt > 0 && event.LeaseEpoch > 0 &&
		event.StartedElapsedNS >= 0 && event.ReturnedElapsedNS >= event.StartedElapsedNS && event.ElapsedNS == event.ReturnedElapsedNS-event.StartedElapsedNS &&
		event.CandidateCount >= 0 && event.MissingCount >= 0 && embeddingJobTimingResultCodeValid(event.ResultCode)
}

type embeddingJobTimingObserver func(embeddingJobTimingEvent)

type embeddingJobTimingScope struct {
	observer embeddingJobTimingObserver
	origin   time.Time
	claim    EmbeddingJobClaim
}

func newEmbeddingJobTimingObserverFromEnvironment() embeddingJobTimingObserver {
	endpoint, err := url.Parse(strings.TrimSpace(os.Getenv(EnvUCIWatcherSLOEmbeddingTimingObserver)))
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || !embeddingJobTimingLoopbackHost(endpoint.Hostname()) {
		return nil
	}
	client := &http.Client{Timeout: embeddingJobTimingObserverTimeout}
	return func(event embeddingJobTimingEvent) {
		if !event.valid() {
			return
		}
		payload, err := json.Marshal(event)
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

func newEmbeddingJobTimingScope(observer embeddingJobTimingObserver, claim EmbeddingJobClaim, origin time.Time) *embeddingJobTimingScope {
	if observer == nil || origin.IsZero() {
		return nil
	}
	return &embeddingJobTimingScope{observer: observer, origin: origin, claim: claim}
}

func (scope *embeddingJobTimingScope) observe(stage embeddingJobTimingStage, started, returned time.Time, batch *EmbeddingBatch, resultCode string) {
	if scope == nil || scope.observer == nil || started.Before(scope.origin) || returned.Before(started) {
		return
	}
	event := embeddingJobTimingEvent{
		Stage:             stage,
		JobDigest:         embeddingJobTimingDigest(scope.claim.Ref.JobID),
		ViewID:            scope.claim.Ref.Context.ViewID,
		Generation:        scope.claim.Ref.Context.Generation,
		ProfileDigest:     embeddingJobTimingDigest(scope.claim.Ref.Context.AnalysisProfileID),
		Attempt:           scope.claim.Attempt,
		LeaseEpoch:        scope.claim.Ref.LeaseEpoch,
		StartedElapsedNS:  started.Sub(scope.origin).Nanoseconds(),
		ReturnedElapsedNS: returned.Sub(scope.origin).Nanoseconds(),
		ElapsedNS:         returned.Sub(started).Nanoseconds(),
		ResultCode:        resultCode,
	}
	if batch != nil {
		event.CandidateCount = len(batch.Candidates)
		event.MissingCount = len(batch.MissingInputIndexes)
	}
	if event.valid() {
		scope.observer(event)
	}
}

func embeddingJobTimingDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func embeddingJobTimingDigestValid(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func embeddingJobTimingResultCode(err error) string {
	if err == nil {
		return "ok"
	}
	return string(classifyEmbeddingFailure(err).Code)
}

func embeddingJobTimingAuthorizationResultCode(err error) string {
	if err == nil {
		return "ok"
	}
	return string(EmbeddingFailureAuthorityLost)
}

func embeddingJobTimingResultCodeValid(value string) bool {
	return value == "ok" || EmbeddingFailureCode(value).valid()
}

func embeddingJobTimingLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
