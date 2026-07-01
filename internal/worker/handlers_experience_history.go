package worker

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
)

type experienceHistoryProvider interface {
	QueryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error)
}

func (s *Service) currentExperienceHistoryProvider() experienceHistoryProvider {
	s.initMu.RLock()
	defer s.initMu.RUnlock()
	return s.experienceProvider
}

// handleExperienceHistoryRead exposes the CR-009 read-only experience/history
// surface over the first-class ExperienceProvider seam.
func (s *Service) handleExperienceHistoryRead(w http.ResponseWriter, r *http.Request) {
	request, err := experienceHistoryReadRequestFromHTTP(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response, err := experiencehistory.ReadHistory(r.Context(), s.currentExperienceHistoryProvider(), request, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, experienceHistoryHTTPStatus(response.State), response)
}

// handleExperienceHistoryDetail exposes one read-only experience detail. It does
// not mutate memory, force reuse, or search archive without a named trigger.
func (s *Service) handleExperienceHistoryDetail(w http.ResponseWriter, r *http.Request) {
	request, err := experienceHistoryDetailRequestFromHTTP(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response, err := experiencehistory.ReadHistoryDetail(r.Context(), s.currentExperienceHistoryProvider(), request, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSONStatus(w, experienceHistoryHTTPStatus(response.State), response)
}

func experienceHistoryReadRequestFromHTTP(r *http.Request) (cognitive.ExperienceQueryRequest, error) {
	query := r.URL.Query()
	queryText := strings.TrimSpace(query.Get("query_text"))
	if queryText == "" {
		queryText = strings.TrimSpace(query.Get("query"))
	}
	limit, err := experienceHistoryLimitFromQuery(query.Get("limit"))
	if err != nil {
		return cognitive.ExperienceQueryRequest{}, err
	}
	return cognitive.ExperienceQueryRequest{
		Project:               strings.TrimSpace(query.Get("project")),
		Principal:             strings.TrimSpace(query.Get("principal")),
		Domain:                strings.TrimSpace(query.Get("domain")),
		Query:                 queryText,
		CurrentContext:        strings.TrimSpace(query.Get("current_context")),
		Situation:             strings.TrimSpace(query.Get("situation")),
		Decision:              strings.TrimSpace(query.Get("decision")),
		Action:                strings.TrimSpace(query.Get("action")),
		Outcome:               strings.TrimSpace(query.Get("outcome")),
		Revision:              strings.TrimSpace(query.Get("revision")),
		Reversal:              strings.TrimSpace(query.Get("reversal")),
		ArchiveTriggerClasses: experienceHistoryTriggersFromHTTP(r),
		Limit:                 limit,
	}, nil
}

func experienceHistoryDetailRequestFromHTTP(r *http.Request) (experiencehistory.HistoryDetailRequest, error) {
	query := r.URL.Query()
	experienceID := strings.TrimSpace(chi.URLParam(r, "experienceID"))
	if experienceID == "" {
		experienceID = strings.TrimSpace(query.Get("experience_id"))
	}
	return experiencehistory.HistoryDetailRequest{
		Project:               strings.TrimSpace(query.Get("project")),
		Principal:             strings.TrimSpace(query.Get("principal")),
		Domain:                strings.TrimSpace(query.Get("domain")),
		ExperienceID:          experienceID,
		CurrentContext:        strings.TrimSpace(query.Get("current_context")),
		ArchiveTriggerClasses: experienceHistoryTriggersFromHTTP(r),
	}, nil
}

func experienceHistoryLimitFromQuery(raw string) (int, error) {
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

func experienceHistoryTriggersFromHTTP(r *http.Request) []cognitive.ExperienceArchiveTriggerClass {
	query := r.URL.Query()
	seen := map[cognitive.ExperienceArchiveTriggerClass]bool{}
	add := func(value string) {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				seen[cognitive.ExperienceArchiveTriggerClass(part)] = true
			}
		}
	}
	add(query.Get("trigger_class"))
	for _, value := range query["archive_trigger_classes"] {
		add(value)
	}
	if explicitArchiveLookupFromQuery(query.Get("explicit_archive_lookup")) {
		seen[cognitive.ExperienceArchiveTriggerExplicitLookup] = true
	}
	out := make([]cognitive.ExperienceArchiveTriggerClass, 0, len(seen))
	for trigger := range seen {
		out = append(out, trigger)
	}
	return out
}

func explicitArchiveLookupFromQuery(raw string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && value
}

func experienceHistoryHTTPStatus(state experiencehistory.HistoryState) int {
	switch state {
	case experiencehistory.HistoryStateGated:
		return http.StatusServiceUnavailable
	case experiencehistory.HistoryStateError:
		return http.StatusInternalServerError
	default:
		return http.StatusOK
	}
}
