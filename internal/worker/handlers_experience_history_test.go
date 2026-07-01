package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
)

func TestHandleExperienceHistoryReadReturnsFirstClassEnvelope(t *testing.T) {
	candidate := restExperienceCandidate("blocked-rest", "POSIX quoting must not be reused in PowerShell retry commands.")
	candidate.AntiApplicability = []cognitive.ExperienceAntiApplicability{
		{
			Condition: "Windows PowerShell command target",
			Rationale: "PowerShell command quoting differs; block silent reuse.",
		},
	}
	service := &Service{experienceProvider: experiencehistory.NewService([]cognitive.ExperienceResponse{candidate})}
	req := httptest.NewRequest(http.MethodGet, "/api/experience-history?project=engram&query_text=PowerShell+retry&current_context=Windows+PowerShell+command+target&limit=1", nil)
	w := httptest.NewRecorder()

	service.handleExperienceHistoryRead(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response experiencehistory.HistoryReadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, experiencehistory.HistoryStateBlockedApplicability, response.State)
	require.Len(t, response.Results, 1)
	require.Equal(t, "session:blocked-rest", response.Results[0].ExperienceID)
	require.Equal(t, cognitive.ExperienceApplicabilityBlocked, response.Results[0].ApplicabilityOutcome)
	require.Equal(t, cognitive.ExperienceSourceProjection, response.Results[0].StorageOrigin)
	require.Equal(t, "archive_skipped", response.ArchiveTrace.Status)
}

func TestHandleExperienceHistoryReadGatedWhenProviderMissing(t *testing.T) {
	service := &Service{}
	req := httptest.NewRequest(http.MethodGet, "/api/experience-history?project=engram&query_text=retry", nil)
	w := httptest.NewRecorder()

	service.handleExperienceHistoryRead(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var response experiencehistory.HistoryReadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, experiencehistory.HistoryStateGated, response.State)
	require.Contains(t, response.Error, "experience provider not configured")
}

func TestHandleExperienceHistoryDetailReturnsReadOnlyDetail(t *testing.T) {
	service := &Service{experienceProvider: experiencehistory.NewService([]cognitive.ExperienceResponse{
		restExperienceCandidate("detail-rest", "Detail should include lesson, provenance, and storage origin."),
	})}
	req := httptest.NewRequest(http.MethodGet, "/api/experience-history/session:detail-rest?project=engram&current_context=detail+lookup", nil)
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Get("/api/experience-history/{experienceID}", service.handleExperienceHistoryDetail)

	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var response experiencehistory.HistoryDetailResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, experiencehistory.HistoryStateLive, response.State)
	require.NotNil(t, response.ExperienceDetail)
	require.Equal(t, "session:detail-rest", response.ExperienceDetail.ExperienceID)
	require.Equal(t, cognitive.ExperienceSourceProjection, response.StorageOrigin)
	require.Contains(t, response.ProvenanceRefs, "session:detail-rest")
}

func TestHandleExperienceHistoryReadRejectsUnnamedArchiveTrigger(t *testing.T) {
	service := &Service{experienceProvider: experiencehistory.NewService(nil)}
	req := httptest.NewRequest(http.MethodGet, "/api/experience-history?project=engram&query_text=history&archive_trigger_classes=always_on_archive", nil)
	w := httptest.NewRecorder()

	service.handleExperienceHistoryRead(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid archive trigger class")
}

func restExperienceCandidate(id, lesson string) cognitive.ExperienceResponse {
	return cognitive.ExperienceResponse{
		Source:        cognitive.ExperienceSourceProjection,
		StorageOrigin: cognitive.ExperienceSourceProjection,
		Situation:     "prior retry investigation",
		Decision:      "retry once before cooldown",
		Action:        "add focused retry proof",
		Outcome:       "regression stayed bounded",
		Lesson:        lesson,
		SourceAttribution: []cognitive.ExperienceSourceAttribution{
			{Kind: "session", ID: id, Project: "engram", SessionID: "s-rest", CreatedAt: time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC)},
		},
	}
}
