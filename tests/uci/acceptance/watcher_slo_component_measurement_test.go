package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/uci"
)

const (
	uciComponentPublicationRecordPathEnv = "ENGRAM_UCI_COMPONENT_PUBLICATION_RECORD_PATH"
	uciComponentPublicationSchemaVersion = "engram.uci-component-publication/v1"
)

// TestUCIRecordComponentPublicationMeasurement measures the direct publication
// components under a deterministic fixture. It intentionally bypasses the
// installed watcher, so its record uses a distinct schema and can never serve
// as ENGRAM_UCI_WATCHER_SLO_INPUT release evidence.
func TestUCIRecordComponentPublicationMeasurement(t *testing.T) {
	recordPath := strings.TrimSpace(os.Getenv(uciComponentPublicationRecordPathEnv))
	if recordPath == "" {
		t.Skipf("%s not set; skipping direct component publication measurement", uciComponentPublicationRecordPathEnv)
	}
	if !filepath.IsAbs(recordPath) {
		t.Fatal("component publication record path must be absolute")
	}
	if strings.TrimSpace(os.Getenv("DATABASE_DSN")) == "" {
		t.Skip("DATABASE_DSN not set; component publication measurement requires a caller-supplied disposable PostgreSQL database")
	}

	fixture := newUCIRetrievalSliceFixture(t)
	corpus, err := newUCISLORecordCorpus(t, fixture)
	if err != nil {
		t.Fatalf("publish component measurement corpus: %v", err)
	}

	attempts := make([]uciComponentPublicationAttempt, 0, uciWatcherSLOMinimumHealthyWarm+1)
	for sequence := 1; len(attempts) < uciWatcherSLOMinimumHealthyWarm+1; sequence++ {
		warmth := "warm"
		if sequence == 1 {
			warmth = "cold"
		}
		attempt, measureErr := uciMeasureComponentPublication(corpus, sequence, warmth)
		if measureErr != nil {
			t.Fatalf("measure direct component publication %d: %v", sequence, measureErr)
		}
		attempts = append(attempts, attempt)
	}

	record := uciComponentPublicationRecord{
		SchemaVersion:     uciComponentPublicationSchemaVersion,
		MeasurementScope:  "direct_component_publication",
		DeterministicOnly: true,
		Attempts:          attempts,
	}
	if err := uciWriteComponentPublicationRecord(recordPath, record); err != nil {
		t.Fatalf("write component publication record: %v", err)
	}
}

type uciComponentPublicationRecord struct {
	SchemaVersion     string                           `json:"schema_version"`
	MeasurementScope  string                           `json:"measurement_scope"`
	DeterministicOnly bool                             `json:"deterministic_only"`
	Attempts          []uciComponentPublicationAttempt `json:"attempts"`
}

type uciComponentPublicationAttempt struct {
	ID                           string        `json:"id"`
	Warmth                       string        `json:"warmth"`
	ChangedBytes                 int64         `json:"changed_bytes"`
	DirectPublicationLatency     time.Duration `json:"direct_publication_latency"`
	DirectStructuralQueryLatency time.Duration `json:"direct_structural_query_latency"`
	DirectEmbeddingLatency       time.Duration `json:"direct_embedding_latency"`
	DeterministicRequestsBefore  int64         `json:"deterministic_requests_before"`
	DeterministicRequestsAfter   int64         `json:"deterministic_requests_after"`
	DeterministicCorpusBefore    int           `json:"deterministic_corpus_before"`
	DeterministicCorpusAfter     int           `json:"deterministic_corpus_after"`
}

func uciMeasureComponentPublication(corpus *uciSLORecordCorpus, sequence int, warmth string) (uciComponentPublicationAttempt, error) {
	started := time.Now().UTC()
	next := uciSLOMarkdownSource(sequence)
	changedBytes := uciSLOChangedBytes(corpus.markdownBytes, next)
	before := uciSLOProviderCalls(corpus.fixture.provider)

	publicationCtx, cancelPublication := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordStructuralWaitBound)
	after, err := corpus.publishUpdate(publicationCtx, sequence)
	publicationCompleted := time.Now().UTC()
	cancelPublication()
	if err != nil {
		return uciComponentPublicationAttempt{}, fmt.Errorf("direct publish: %w", err)
	}
	corpus.current = after

	authorized, err := corpus.authorize(corpus.fixture.callerContext)
	if err != nil {
		return uciComponentPublicationAttempt{}, fmt.Errorf("authorize direct component query: %w", err)
	}
	queryCtx, cancelQuery := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordStructuralWaitBound)
	response, err := corpus.fixture.queryService.Query(queryCtx, authorized, uci.QuerySpec{
		ClientSessionID: corpus.fixture.clientSessionID,
		Mode:            uci.QueryModeFTS,
		Text:            uciSLOUpdateMarker(sequence),
		Order:           uci.QueryOrderRelevance,
		Limit:           10,
	})
	queryCompleted := time.Now().UTC()
	cancelQuery()
	if err != nil || response.Response.Status != uci.QueryStatusOK {
		return uciComponentPublicationAttempt{}, fmt.Errorf("direct structural query status=%s error=%v", response.Response.Status, err)
	}

	embeddingCtx, cancelEmbedding := context.WithTimeout(corpus.fixture.callerContext, uciSLORecordEmbeddingWaitBound)
	err = uciSLOEnsureCurrentViewEmbedding(embeddingCtx, corpus.fixture, authorized)
	embeddingCompleted := time.Now().UTC()
	cancelEmbedding()
	if err != nil {
		return uciComponentPublicationAttempt{}, fmt.Errorf("direct embedding: %w", err)
	}
	afterCounters := uciSLOProviderCalls(corpus.fixture.provider)

	return uciComponentPublicationAttempt{
		ID:                           fmt.Sprintf("component-%s-%03d", warmth, sequence),
		Warmth:                       warmth,
		ChangedBytes:                 changedBytes,
		DirectPublicationLatency:     publicationCompleted.Sub(started),
		DirectStructuralQueryLatency: queryCompleted.Sub(started),
		DirectEmbeddingLatency:       embeddingCompleted.Sub(started),
		DeterministicRequestsBefore:  int64(before.requests),
		DeterministicRequestsAfter:   int64(afterCounters.requests),
		DeterministicCorpusBefore:    before.corpus,
		DeterministicCorpusAfter:     afterCounters.corpus,
	}, nil
}

func uciWriteComponentPublicationRecord(path string, record uciComponentPublicationRecord) error {
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uci-component-publication-")
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
