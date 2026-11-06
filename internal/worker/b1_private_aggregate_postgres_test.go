package worker

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/embedding"
	"github.com/thebtf/engram/pkg/models"
)

// Uses only public HTTP reads and the existing isolated PostgreSQL fixture;
// vector chunks are created through the existing store, not a provider.
func TestB1PrivateAggregatesPostgres(t *testing.T) {
	for _, kind := range []string{models.MemoryContextSelf, models.MemoryContextGlobal} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnCardContextFixture(t, false)
			owner := f.registered(t, "aggregate-owner")
			reader := f.registered(t, "aggregate-reader")
			emb := embedding.NewStore(f.store.DB)
			f.service.embeddingStore = emb
			control := f.create(t, reader, ownCardContextPayload("reader-visible aggregate control"))
			vector := make([]float32, embedding.EmbeddingDim)
			vector[0] = 1
			addChunk := func(card ownCardContextRecord, model string) {
				t.Helper()
				require.NoError(t, emb.StoreChunks(t.Context(), []embedding.Chunk{{MemoryID: card.ID, Seq: 0, Text: card.Content, Model: model, Embedding: pgvector.NewVector(vector)}}))
			}
			addChunk(control, "synthetic-visible-model")
			stats := func(caller ownCardContextCaller) vnextStatsResponse {
				t.Helper()
				response := f.call(t, http.MethodGet, "/api/stats/vnext", nil, caller)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var value vnextStatsResponse
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &value))
				require.NotNil(t, value.Embedding)
				return value
			}
			metrics := func(caller ownCardContextCaller) embedding.EmbeddingStats {
				t.Helper()
				response := f.call(t, http.MethodGet, "/api/vector/metrics", nil, caller)
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var value struct {
					Stats embedding.EmbeddingStats `json:"stats"`
				}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &value))
				return value.Stats
			}
			beforeReader, beforeOwner := stats(reader.caller()), stats(owner.caller())
			beforeAdmin := stats(f.admin)
			beforeMetrics := metrics(reader.caller())
			require.Positive(t, beforeReader.WriteGateStats["active"], "reader must see its own control")
			require.Positive(t, beforeMetrics.ChunkCount, "reader must see its own vector control")
			payload := ownCardContextPayload("hidden aggregate card")
			payload["context"] = map[string]any{"kind": kind}
			payload["agent_visibility"] = models.AgentVisibilityPrivate
			hidden := f.create(t, owner, payload)
			addChunk(hidden, "synthetic-hidden-model")
			require.NoError(t, gormdb.NewCitationLogStore(f.store).RecordBatch(t.Context(), []gormdb.CitationRecord{{SessionID: "synthetic-hidden-event", MemoryID: hidden.ID, Cited: true}}))
			afterReader, afterOwner := stats(reader.caller()), stats(owner.caller())
			afterMetrics := metrics(reader.caller())
			assert.Equal(t, beforeReader.WriteGateStats, afterReader.WriteGateStats, "hidden card must not change reader status counts")
			assert.Equal(t, beforeReader.Embedding.ActiveMemoryCount, afterReader.Embedding.ActiveMemoryCount, "hidden card must not change reader coverage denominator")
			assert.Equal(t, beforeReader.Embedding.ChunkCount, afterReader.Embedding.ChunkCount, "hidden vector must not change reader chunk count")
			assert.Equal(t, beforeMetrics.ChunkCount, afterMetrics.ChunkCount, "vector endpoint must not count hidden chunks")
			assert.Equal(t, beforeMetrics.Model, afterMetrics.Model, "vector endpoint must not reveal the hidden model")
			assert.Equal(t, beforeMetrics.LastChunkAt, afterMetrics.LastChunkAt, "vector endpoint must not reveal hidden write times")
			assert.Equal(t, beforeOwner.WriteGateStats["active"]+1, afterOwner.WriteGateStats["active"], "owner can observe its own newly created card")
			assert.Equal(t, beforeReader.CitationCount, afterReader.CitationCount, "hidden citation must not alter reader event totals")
			admin := stats(f.admin)
			require.Equal(t, beforeAdmin.CitationCount+1, admin.CitationCount, "admin retains global citation totals")
			require.NoError(t, f.store.DB.Exec(`UPDATE memories SET deleted_at = now() WHERE id = ?`, hidden.ID).Error)
			require.Equal(t, admin.CitationCount, stats(f.admin).CitationCount, "admin's historical citation total includes deleted memories")
			require.Equal(t, beforeReader.CitationCount, stats(reader.caller()).CitationCount, "deleted hidden citation remains invisible to reader")
		})
	}
}

func TestB1PrivateProcessTelemetryPostgres(t *testing.T) {
	f := newOwnCardContextFixture(t, false)
	reader := f.registered(t, "telemetry-reader")
	f.service.embeddingStore = embedding.NewStore(f.store.DB)
	recorder := &embedding.BackfillRecorder{}
	f.service.embeddingRecorder = recorder
	read := func() (map[string]any, string) {
		t.Helper()
		response := f.call(t, http.MethodGet, "/api/stats/vnext", nil, reader.caller())
		require.Equal(t, http.StatusOK, response.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		telemetry, ok := body["embedding"].(map[string]any)
		require.True(t, ok, "database telemetry must remain available")
		return telemetry, response.Body.String()
	}
	before, _ := read()
	recorder.RecordFailure(0, "synthetic-private-provider-error")
	after, body := read()
	assert.Equal(t, before["embed_failure_count"], after["embed_failure_count"], "unattributed process counts are not caller-scoped")
	assert.Equal(t, before["last_embed_error"], after["last_embed_error"], "unattributed errors are not caller-scoped")
	assert.NotContains(t, body, "synthetic-private-provider-error")
}
