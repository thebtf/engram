package worker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/cognitive"
)

const temporalTruthFeatureFlag = "ENGRAM_TEMPORAL_TRUTH_ENABLED"

type temporalTruthProvider interface {
	QueryTemporalTruth(ctx context.Context, request cognitive.TemporalTruthQueryRequest) (cognitive.TemporalTruthResponse, error)
	RefreshProject(ctx context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error)
}

func temporalTruthEnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv(temporalTruthFeatureFlag))
	return value == "true" || value == "1"
}

func (s *Service) temporalTruthActive() bool {
	return s != nil && s.temporalTruthEnabled
}

func (s *Service) currentTemporalTruthProvider() temporalTruthProvider {
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.temporalTruthProvider
}

func (s *Service) rejectTemporalTruthDisabled(w http.ResponseWriter) bool {
	if s.temporalTruthActive() {
		return false
	}
	http.Error(w, "temporal truth feature flag required", http.StatusServiceUnavailable)
	return true
}

// handleTemporalTruthRead exposes the CR-011 bounded temporal truth read seam.
func (s *Service) handleTemporalTruthRead(w http.ResponseWriter, r *http.Request) {
	if s.rejectTemporalTruthDisabled(w) {
		return
	}
	provider := s.currentTemporalTruthProvider()
	if provider == nil {
		http.Error(w, "temporal truth provider not configured", http.StatusServiceUnavailable)
		return
	}
	request, err := temporalTruthRequestFromHTTP(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response, err := provider.QueryTemporalTruth(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, response)
}

// handleTemporalTruthRefresh exposes the explicit CR-011 admission trigger.
func (s *Service) handleTemporalTruthRefresh(w http.ResponseWriter, r *http.Request) {
	if s.rejectTemporalTruthDisabled(w) {
		return
	}
	provider := s.currentTemporalTruthProvider()
	if provider == nil {
		http.Error(w, "temporal truth provider not configured", http.StatusServiceUnavailable)
		return
	}
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	result, err := provider.RefreshProject(r.Context(), project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func temporalTruthRequestFromHTTP(r *http.Request) (cognitive.TemporalTruthQueryRequest, error) {
	query := r.URL.Query()
	project := strings.TrimSpace(query.Get("project"))
	if project == "" {
		return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("project is required")
	}
	factID := strings.TrimSpace(query.Get("fact_id"))
	if factID == "" {
		return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("fact_id is required")
	}
	limit, err := temporalTruthLimitFromQuery(query.Get("limit"))
	if err != nil {
		return cognitive.TemporalTruthQueryRequest{}, err
	}
	request := cognitive.TemporalTruthQueryRequest{
		Project:   project,
		FactID:    factID,
		FactClass: strings.TrimSpace(query.Get("fact_class")),
		Limit:     limit,
	}
	if rawAsOf := strings.TrimSpace(query.Get("as_of")); rawAsOf != "" {
		parsed, err := time.Parse(time.RFC3339, rawAsOf)
		if err != nil {
			return cognitive.TemporalTruthQueryRequest{}, fmt.Errorf("as_of must be RFC3339")
		}
		request.AsOf = &parsed
	}
	return request, nil
}

func temporalTruthLimitFromQuery(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, strconv.ErrSyntax
	}
	return limit, nil
}
