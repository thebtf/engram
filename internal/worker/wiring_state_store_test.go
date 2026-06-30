package worker

import (
	"testing"

	localgorm "github.com/thebtf/engram/internal/db/gorm"
)

func TestWireStateStoreRecordsServiceSeam(t *testing.T) {
	svc := &Service{}
	stateStore := &localgorm.StateStore{}

	wireStateStore(svc, stateStore)

	svc.initMu.RLock()
	got := svc.stateStore
	svc.initMu.RUnlock()
	if got != stateStore {
		t.Fatalf("stateStore seam not recorded on Service")
	}
}
