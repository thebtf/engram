package reaper

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestReaperStopBeforeStartReturns(t *testing.T) {
	r := newTestReaper(t, nil)
	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked before Start")
	}
}

func TestReaperRepeatedLifecycleCallsAreSafe(t *testing.T) {
	r := newTestReaper(t, nil)
	r.Start(context.Background())
	r.Stop()
	r.Stop()
	r.Start(context.Background())
	r.Stop()
}

func TestRetentionDurationRejectsOverflow(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT_RETENTION_DAYS", strconv.FormatInt(maxRetentionDays+1, 10))

	if _, err := retentionDuration(); err == nil {
		t.Fatal("retention duration accepted an overflowing number of days")
	}
}

func TestReaperConstructionRejectsOverflow(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT_RETENTION_DAYS", strconv.FormatInt(maxRetentionDays+1, 10))

	if _, err := New(nil); err == nil {
		t.Fatal("New accepted an overflowing number of retention days")
	}
}
