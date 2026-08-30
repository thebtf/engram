package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/legacyrelay"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

type relayTestHandler struct{}

func (*relayTestHandler) HandleRequest(context.Context, muxcore.ProjectContext, []byte) ([]byte, error) {
	return []byte(`{"ok":true}`), nil
}

func TestPrepareLegacyRelayDefaultOffPreservesExactHandler(t *testing.T) {
	t.Setenv(config.EnvHAP01BRelayEnabled, "")
	next := &relayTestHandler{}
	runtime, err := prepareLegacyRelay(engramcore.NewModule(), next, "")
	if err != nil {
		t.Fatalf("prepare default-off relay: %v", err)
	}
	if runtime.handler != next || runtime.gateway != nil || runtime.listener != nil {
		t.Fatalf("default-off runtime = %#v", runtime)
	}
	baseDir, err := legacyRelayRuntimeBaseDir()
	if err != nil || baseDir != "" {
		t.Fatalf("default-off runtime baseDir=%q err=%v", baseDir, err)
	}
}

func TestPrepareLegacyRelayEnabledRequiresExactArtifactDigest(t *testing.T) {
	t.Setenv(config.EnvHAP01BRelayEnabled, "true")
	t.Setenv(config.EnvHAP01BRelayRevision, legacyrelay.AdapterRevisionHAP01B)
	baseDir, err := legacyRelayRuntimeBaseDir()
	if err != nil || baseDir == "" {
		t.Fatalf("enabled per-user runtime baseDir=%q err=%v", baseDir, err)
	}
	t.Setenv(config.EnvHAP01BAdapterSHA256, "")
	if _, err := prepareLegacyRelay(engramcore.NewModule(), &relayTestHandler{}, t.TempDir()); err == nil {
		t.Fatal("enabled relay accepted missing artifact digest")
	}
}

func TestLegacyRelayRuntimeStartsAndCleansPrivateLocator(t *testing.T) {
	t.Setenv(config.EnvHAP01BRelayEnabled, "true")
	t.Setenv(config.EnvHAP01BRelayRevision, legacyrelay.AdapterRevisionHAP01B)
	t.Setenv(config.EnvHAP01BAdapterSHA256, strings.Repeat("a", 64))
	runtimeDir := t.TempDir()
	runtime, err := prepareLegacyRelay(engramcore.NewModule(), &relayTestHandler{}, runtimeDir)
	if err != nil {
		t.Fatalf("prepare enabled relay: %v", err)
	}
	if runtime.baseDir != runtimeDir {
		t.Fatalf("relay baseDir=%q, want %q", runtime.baseDir, runtimeDir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runtime.start(ctx, "daemon-test-generation"); err != nil {
		t.Fatalf("start relay runtime: %v", err)
	}
	locatorPath := legacyrelay.RuntimeLocatorPath(runtime.baseDir)
	if _, err := os.Stat(locatorPath); err != nil {
		t.Fatalf("stat relay locator: %v", err)
	}
	if err := runtime.close(); err != nil {
		t.Fatalf("close relay runtime: %v", err)
	}
	if _, err := os.Stat(locatorPath); !os.IsNotExist(err) {
		t.Fatalf("closed relay locator stat error = %v, want not exist", err)
	}
}
