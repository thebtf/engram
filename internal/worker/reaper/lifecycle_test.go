package reaper

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestReaperStopBeforeStartReturns(t *testing.T) {
	r := New(nil)
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
	r := New(nil)
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
