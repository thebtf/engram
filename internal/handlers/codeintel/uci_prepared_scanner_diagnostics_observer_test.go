package codeintel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUCIPreparedScannerAggregateObserverPostsAllowlistedDiagnostics(t *testing.T) {
	t.Setenv(EnvUCIWatcherSLOScannerAggregateObserver, "")
	if observer := newUCIPreparedScannerAggregateObserverFromEnvironment(); observer != nil {
		t.Fatal("unset scanner aggregate observer was enabled")
	}

	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("observer method = %s, want POST", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var err error
		received, err = io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read observer payload: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv(EnvUCIWatcherSLOScannerAggregateObserver, server.URL)
	observer := newUCIPreparedScannerAggregateObserverFromEnvironment()
	if observer == nil {
		t.Fatal("loopback scanner aggregate observer was not enabled")
	}
	observer(uciPreparedScannerAggregate{
		SourceID: "source-id", CheckoutID: "checkout-id", ProfileID: "profile-id", ObservedFSSeq: 41,
		ScanStartedAt:         time.Date(2026, time.September, 9, 12, 0, 0, 123, time.UTC),
		ScanCompletedAt:       time.Date(2026, time.September, 9, 12, 0, 0, 456, time.UTC),
		GitTopologyDurationNS: 11, GitStatusDurationNS: 13, GitStagedDurationNS: 17, GitUntrackedDurationNS: 19,
		CandidateLoopDurationNS: 23, ScanTotalDurationNS: 101, ResidualDurationNS: 18,
		CandidateCount: 29, AdmittedCount: 31, ExcludedCount: 37, UnreadableCount: 41, BytesRead: 43,
	})
	payload := string(received)
	for _, expected := range []string{`"source_id":"source-id"`, `"checkout_id":"checkout-id"`, `"profile_id":"profile-id"`, `"observed_fs_seq":41`, `"scan_total_duration_ns":101`, `"bytes_read":43`} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("scanner observer payload omitted %q: %s", expected, payload)
		}
	}
	for _, forbidden := range []string{"root_path", "sensitive/path.go", "secret-body-do-not-record"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("scanner observer payload retained %q: %s", forbidden, payload)
		}
	}
}
