package legacyrelay

import (
	"os"
	"testing"
)

func TestOSProcessInspectorBindsCurrentProcessIncarnation(t *testing.T) {
	process, err := NewOSProcessInspector().Inspect(os.Getpid())
	if err != nil {
		t.Fatalf("Inspect current process: %v", err)
	}
	if !process.valid() || process.PID() != os.Getpid() || process.Image().Value() == "" {
		t.Fatalf("process identity = %#v", process)
	}
	again, err := NewOSProcessInspector().Inspect(os.Getpid())
	if err != nil {
		t.Fatalf("Inspect current process again: %v", err)
	}
	if !sameProcess(process, again) {
		t.Fatalf("current process incarnation drifted: first=%#v second=%#v", process, again)
	}
}
