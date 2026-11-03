package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

const (
	feature009ProducerBudgetHeader = "X-Engram-Budget-Ms"
	feature009ProducerProject      = "11111111-1111-4111-8111-111111111111"
	feature009ProducerMaxSafeID    = int64(9007199254740991)
)

type feature009ProducerObservation struct {
	ID        json.RawMessage `json:"id"`
	Type      string          `json:"type"`
	Project   string          `json:"project"`
	Narrative string          `json:"narrative"`
	SourceRef json.RawMessage `json:"source_ref"`
}

type feature009ProducerResponse struct {
	Project      string                          `json:"project"`
	Query        string                          `json:"query"`
	Observations []feature009ProducerObservation `json:"observations"`
	AlwaysInject []feature009ProducerObservation `json:"always_inject"`
}

func feature009ProducerRequest(ctx context.Context, method, base, project, query string, budget *string) (*http.Request, error) {
	values := url.Values{"limit": {"10"}}
	var payload []byte
	if method == http.MethodPost {
		var err error
		payload, err = json.Marshal(map[string]string{"project": project, "query": query})
		if err != nil {
			return nil, err
		}
	} else {
		values.Set("project", project)
		values.Set("query", query)
	}
	request, err := http.NewRequestWithContext(ctx, method, base+"/api/context/search?"+values.Encode(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if budget != nil {
		request.Header.Set(feature009ProducerBudgetHeader, *budget)
	}
	return request, nil
}

// Only the existing retrieval hook observes the processing context here. Real
// SQL, authenticated routes, provider cancellation and useful fallback are
// exercised separately by TestContextSearchProducerFallbackPostgres.
func TestContextSearchProducerBudgetHeader(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			for _, test := range []struct {
				name       string
				value      string
				present    bool
				parentTime time.Duration
				status     int
				shortens   bool
				above500ms bool
			}{
				{name: "absent_preserves_parent", parentTime: 2 * time.Second, status: http.StatusOK},
				{name: "positive_shortens", value: "150", present: true, parentTime: 2 * time.Second, status: http.StatusOK, shortens: true},
				{name: "positive_above_500_is_not_globally_capped", value: "900", present: true, parentTime: 2 * time.Second, status: http.StatusOK, shortens: true, above500ms: true},
				{name: "positive_cannot_lengthen_parent", value: "900", present: true, parentTime: 100 * time.Millisecond, status: http.StatusOK},
				{name: "max_int64_milliseconds_cannot_overflow_or_lengthen", value: "9223372036854775807", present: true, parentTime: 100 * time.Millisecond, status: http.StatusOK},
				{name: "empty", present: true, status: http.StatusBadRequest},
				{name: "nonnumeric", value: "later", present: true, status: http.StatusBadRequest},
				{name: "fractional", value: "1.5", present: true, status: http.StatusBadRequest},
				{name: "zero", value: "0", present: true, status: http.StatusBadRequest},
				{name: "negative", value: "-1", present: true, status: http.StatusBadRequest},
				{name: "int64_overflow", value: "9223372036854775808", present: true, status: http.StatusBadRequest},
			} {
				t.Run(test.name, func(t *testing.T) {
					service := newInjectTestService()
					var observedDeadline time.Time
					var hasDeadline bool
					service.retrievalHooks.retrieveRelevant = func(ctx context.Context, project, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
						observedDeadline, hasDeadline = ctx.Deadline()
						return []*models.Observation{{ID: 42, Project: project}}, nil, nil
					}
					parentTime := test.parentTime
					if parentTime == 0 {
						parentTime = 2 * time.Second
					}
					ctx, cancel := context.WithTimeout(t.Context(), parentTime)
					defer cancel()
					parentDeadline, _ := ctx.Deadline()
					var budget *string
					if test.present {
						budget = &test.value
					}
					request, err := feature009ProducerRequest(ctx, method, "http://example.invalid", feature009ProducerProject, "cobalt lantern", budget)
					require.NoError(t, err)
					writer := httptest.NewRecorder()
					started := time.Now()
					if method == http.MethodGet {
						service.handleSearchByPromptGET(writer, request)
					} else {
						service.handleSearchByPrompt(writer, request)
					}
					finished := time.Now()
					require.Equal(t, test.status, writer.Code)
					if test.status == http.StatusBadRequest {
						control := httptest.NewRecorder()
						badRequest, err := feature009ProducerRequest(ctx, method, "http://example.invalid", "", "cobalt lantern", nil)
						require.NoError(t, err)
						service.handleSearchByPrompt(control, badRequest)
						require.Equal(t, http.StatusBadRequest, control.Code)
						require.Equal(t, control.Header().Get("Content-Type"), writer.Header().Get("Content-Type"))
						require.NotEmpty(t, strings.TrimSpace(writer.Body.String()))
						return
					}
					require.True(t, hasDeadline, "the processing context must retain a deadline")
					require.False(t, observedDeadline.After(parentDeadline), "a client budget may never extend its parent deadline")
					require.True(t, observedDeadline.After(started), "positive parseable budgets must not overflow into an expired duration")
					if test.shortens {
						ms, err := strconv.ParseInt(test.value, 10, 64)
						require.NoError(t, err)
						require.False(t, observedDeadline.After(finished.Add(time.Duration(ms)*time.Millisecond)), "the positive header must bound processing")
						require.True(t, observedDeadline.Before(parentDeadline))
					} else {
						require.True(t, observedDeadline.Equal(parentDeadline), "absent or larger budgets preserve the shorter parent deadline")
					}
					if test.above500ms {
						require.True(t, observedDeadline.After(started.Add(500*time.Millisecond)), "this header adds no global 500ms server cap")
					}
					var response feature009ProducerResponse
					require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &response))
					require.Equal(t, feature009ProducerProject, response.Project)
					require.Equal(t, "cobalt lantern", response.Query)
					require.Len(t, response.Observations, 1)
					require.Equal(t, int64(42), feature009ProducerID(t, response.Observations[0]))
				})
			}
		})
	}
}

func TestContextSearchProducerLegacyObservations(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			service := newInjectTestService()
			service.retrievalHooks.retrieveRelevant = func(_ context.Context, project, _ string, _ RetrievalOptions) ([]*models.Observation, map[int64]float64, error) {
				return []*models.Observation{
					{ID: 42, Project: project, Type: models.ObsTypeDiscovery, Narrative: sql.NullString{String: "Legacy source-less discovery", Valid: true}},
					{ID: 43, Project: project, Type: models.ObsTypeGuidance, Narrative: sql.NullString{String: "Legacy source-less guidance", Valid: true}},
				}, nil, nil
			}
			request, err := feature009ProducerRequest(t.Context(), method, "http://example.invalid", feature009ProducerProject, "legacy", nil)
			require.NoError(t, err)
			writer := httptest.NewRecorder()
			service.handleSearchByPrompt(writer, request)
			require.Equal(t, http.StatusOK, writer.Code)
			var response feature009ProducerResponse
			require.NoError(t, json.Unmarshal(writer.Body.Bytes(), &response))
			require.Equal(t, feature009ProducerProject, response.Project)
			require.Equal(t, "legacy", response.Query)
			require.Len(t, response.Observations, 2)
			for i, observation := range response.Observations {
				require.Equal(t, int64(42+i), feature009ProducerID(t, observation))
				require.NotEmpty(t, observation.Narrative)
				require.Empty(t, observation.SourceRef, "source-less results must omit source_ref, not fabricate a store from the observation type")
			}
		})
	}
}

func feature009ProducerID(t *testing.T, observation feature009ProducerObservation) int64 {
	t.Helper()
	id, err := strconv.ParseInt(string(observation.ID), 10, 64)
	require.NoError(t, err, "the existing observation id must remain an integer JSON number, without float coercion")
	return id
}
