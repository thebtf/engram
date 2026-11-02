package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/operability"
)

func TestHandleGetSearchAnalyticsUnavailableIsNotComputable(t *testing.T) {
	svc := &Service{}
	req := httptest.NewRequest(http.MethodGet, "/api/search/analytics", nil)
	w := httptest.NewRecorder()

	svc.handleGetSearchAnalytics(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusOK)
	}
	var response struct {
		TotalSearches              int64                    `json:"total_searches"`
		ZeroResultRate             *float64                 `json:"zero_result_rate"`
		ZeroResultRateResultStatus operability.ResultStatus `json:"zero_result_rate_result_status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if response.TotalSearches != 0 || response.ZeroResultRate != nil || response.ZeroResultRateResultStatus != operability.NotComputable {
		t.Fatalf("response=%#v, want zero observations with null/not_computable rate", response)
	}
}

func TestNewSearchAnalyticsResponsePreservesMeasuredZeroRate(t *testing.T) {
	response := newSearchAnalyticsResponse(&gorm.SearchAnalytics{TotalSearches: 3, ZeroResultRate: 0})

	if response.ZeroResultRate == nil || *response.ZeroResultRate != 0 || response.ZeroResultRateResultStatus != operability.Computed {
		t.Fatalf("response=%#v, want measured zero/computed rate", response)
	}
}

func TestSearchAnalyticsMetricTruth(t *testing.T) {
	tests := []struct {
		name      string
		analytics *gorm.SearchAnalytics
		latency   any
		status    string
	}{
		{name: "no_store_actual_HTTP", status: "not_computable"},
		{
			name: "searches_without_positive_latency_samples",
			analytics: &gorm.SearchAnalytics{
				TotalSearches: 8, SearchesToday: 2, FilterSearches: 8, ZeroResultRate: 0.25,
			},
			status: "not_computable",
		},
		{
			name: "positive_latency_without_error_instrumentation",
			analytics: &gorm.SearchAnalytics{
				TotalSearches: 8, SearchesToday: 2, FilterSearches: 8, ZeroResultRate: 0.25, AvgLatencyMs: 12.5,
			},
			latency: float64(12.5), status: "computed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if tt.analytics == nil {
				svc := &Service{}
				svc.handleGetSearchAnalytics(w, httptest.NewRequest(http.MethodGet, "/api/search/analytics", nil))
			} else {
				writeJSON(w, newSearchAnalyticsResponse(tt.analytics))
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d, want %d", w.Code, http.StatusOK)
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			t.Logf("actual wire response: %s", w.Body.String())
			for field, want := range map[string]any{
				"avg_latency_ms":               tt.latency,
				"avg_latency_ms_result_status": tt.status,
				"search_errors":                nil,
				"search_errors_result_status":  "not_computable",
			} {
				got, present := response[field]
				if !present || got != want {
					t.Errorf("%s: present=%t value=%#v, want present=true value=%#v", field, present, got, want)
				}
			}
			var total, today float64
			if tt.analytics != nil {
				total, today = float64(tt.analytics.TotalSearches), float64(tt.analytics.SearchesToday)
			}
			if response["total_searches"] != total || response["searches_today"] != today {
				t.Errorf("search counters=(%v, %v), want (%v, %v)", response["total_searches"], response["searches_today"], total, today)
			}
		})
	}
}
