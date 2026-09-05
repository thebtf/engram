package main

import (
	"context"
	"errors"
	"testing"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/module/dispatcher"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestMuxcoreDaemonConfigAuthorizesFreshTransportTags(t *testing.T) {
	tags := []string{"transport-tag-one", "transport-tag-two"}
	index := 0
	cfg := muxcoreDaemonConfigWithSessionGenerator(&dispatcher.Dispatcher{}, func() (string, error) {
		tag := tags[index]
		index++
		return tag, nil
	})
	if cfg.AuthorizeSession == nil {
		t.Fatal("daemon configuration omitted AuthorizeSession")
	}
	first := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{ID: "same-host-project"})
	second := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{ID: "same-host-project"})
	if first.Decision != muxcore.AuthAllow || second.Decision != muxcore.AuthAllow {
		t.Fatalf("authorization decisions = %#v, %#v; want both allow", first, second)
	}
	if first.TenantID != tags[0] || second.TenantID != tags[1] || first.TenantID == second.TenantID {
		t.Fatalf("transport tags = %q, %q; want distinct generated tags", first.TenantID, second.TenantID)
	}
	if !auditcontext.ValidUCITransportSession(first.TenantID) || !auditcontext.ValidUCITransportSession(second.TenantID) {
		t.Fatalf("generated tags are not valid transport identities: %#v, %#v", first, second)
	}
}

func TestMuxcoreDaemonConfigDeniesTransportEntropyFailure(t *testing.T) {
	cfg := muxcoreDaemonConfigWithSessionGenerator(&dispatcher.Dispatcher{}, func() (string, error) {
		return "", errors.New("entropy unavailable")
	})
	verdict := cfg.AuthorizeSession(context.Background(), muxcore.ConnInfo{}, muxcore.ProjectContext{})
	if verdict.Decision != muxcore.AuthDeny || verdict.Reason != uciTransportSessionUnavailableReason || verdict.TenantID != "" {
		t.Fatalf("entropy failure verdict = %#v", verdict)
	}
}

func TestNewUCITransportSessionProducesValidOpaqueTag(t *testing.T) {
	tag, err := newUCITransportSession()
	if err != nil {
		t.Fatalf("newUCITransportSession: %v", err)
	}
	if !auditcontext.ValidUCITransportSession(tag) {
		t.Fatalf("generated invalid transport tag %q", tag)
	}
}
