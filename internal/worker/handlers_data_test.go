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
