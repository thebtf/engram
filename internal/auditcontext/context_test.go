package auditcontext

import (
	"context"
	"strings"
	"testing"
)

func TestUCITransportSessionRoundTripIsDistinctFromAuditValues(t *testing.T) {
	ctx := WithActor(context.Background(), "agent/alice")
	ctx = WithSourceSession(ctx, "legacy-source-session")
	ctx = WithUCITransportSession(ctx, "transport-tag-1")

	if got := UCITransportSession(ctx); got != "transport-tag-1" {
		t.Fatalf("UCI transport session = %q, want transport-tag-1", got)
	}
	if got := SourceSession(ctx); got != "legacy-source-session" {
		t.Fatalf("source session = %q, want legacy-source-session", got)
	}
	if got := Actor(ctx); got != "agent/alice" {
		t.Fatalf("actor = %q, want agent/alice", got)
	}
}

func TestUCITransportSessionRejectsInvalidTags(t *testing.T) {
	for _, value := range []string{
		"",
		" leading",
		"trailing ",
		"contains\nnewline",
		strings.Repeat("x", maxUCITransportSessionBytes+1),
		string([]byte{0xff}),
	} {
		if ValidUCITransportSession(value) {
			t.Errorf("ValidUCITransportSession(%q) = true, want false", value)
		}
		ctx := WithUCITransportSession(context.Background(), value)
		if got := UCITransportSession(ctx); got != "" {
			t.Errorf("UCITransportSession(%q) = %q, want empty", value, got)
		}
	}
}
