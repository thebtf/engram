package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/legacyrelay"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	legacyRelayCapabilityTTL = 15 * time.Minute
	legacyRelaySafetyTimeout = 5 * time.Second
	legacyRelayAncestryDepth = 4
)

type legacyRelayRuntime struct {
	handler      muxcore.SessionHandler
	children     *legacyrelay.ChildRegistry
	inspector    legacyrelay.ProcessInspector
	capabilities *legacyrelay.CapabilityRegistry
	gateway      *engramcore.LegacyRelayGateway
	adapter      legacyrelay.AdapterAttestation
	listener     *legacyrelay.Listener
	baseDir      string
}

func prepareLegacyRelay(coreModule *engramcore.Module, next muxcore.SessionHandler, baseDir string) (*legacyRelayRuntime, error) {
	if !config.HAP01BRelayEnabled() {
		return &legacyRelayRuntime{handler: next}, nil
	}
	if coreModule == nil || next == nil || strings.TrimSpace(baseDir) == "" || config.HAP01BRelayRevision() != legacyrelay.AdapterRevisionHAP01B {
		return nil, errors.New("HAP-01B relay configuration is incomplete")
	}
	adapter, err := legacyrelay.NewAdapterAttestation(config.HAP01BRelayRevision(), strings.TrimSpace(os.Getenv(config.EnvHAP01BAdapterSHA256)))
	if err != nil {
		return nil, err
	}
	executable, err := canonicalExecutablePath()
	if err != nil {
		return nil, err
	}
	inspector := legacyrelay.NewOSProcessInspector()
	children := legacyrelay.NewChildRegistry(inspector, legacyrelay.ChildImageGateFunc(func(image legacyrelay.ProcessImage) bool {
		candidate, err := canonicalPath(image.Value())
		if err != nil {
			return false
		}
		if runtime.GOOS == "windows" {
			return strings.EqualFold(candidate, executable)
		}
		return candidate == executable
	}))
	capabilities, err := legacyrelay.NewCapabilityRegistry(legacyrelay.CapabilityRegistryConfig{
		TTL:       legacyRelayCapabilityTTL,
		Now:       time.Now,
		Inspector: inspector,
		Children:  children,
	})
	if err != nil {
		return nil, err
	}
	gateway, err := engramcore.NewLegacyRelayGateway(coreModule, children, capabilities)
	if err != nil {
		return nil, err
	}
	tracking, err := legacyrelay.NewSessionTrackingHandler(next, children)
	if err != nil {
		return nil, err
	}
	return &legacyRelayRuntime{
		handler:      tracking,
		children:     children,
		inspector:    inspector,
		capabilities: capabilities,
		gateway:      gateway,
		adapter:      adapter,
		baseDir:      filepath.Clean(baseDir),
	}, nil
}

func (r *legacyRelayRuntime) start(ctx context.Context, generationValue string) error {
	if r == nil || r.gateway == nil {
		return nil
	}
	generation, err := legacyrelay.NewDaemonGeneration(generationValue)
	if err != nil {
		return err
	}
	routes, err := legacyrelay.NewRouteSet(legacyrelay.RouteSessionStartContext, legacyrelay.RouteAmbientCandidates)
	if err != nil {
		return err
	}
	relay, err := legacyrelay.NewRelay(legacyrelay.RelayConfig{
		Generation:       generation,
		AdapterGate:      legacyrelay.ExactAdapterGate(r.adapter),
		Bootstrapper:     legacyrelay.NewBootstrapper(r.children, r.inspector, legacyRelayAncestryDepth),
		Capabilities:     r.capabilities,
		Gateway:          r.gateway,
		PeerResolver:     legacyrelay.NewOSPeerResolver(),
		Now:              time.Now,
		SafetyDeadline:   legacyRelaySafetyTimeout,
		MaxFrameBytes:    legacyrelay.DefaultMaxFrameBytes,
		CapabilityRoutes: routes,
	})
	if err != nil {
		return err
	}
	listener, err := legacyrelay.StartListener(ctx, legacyrelay.ListenerConfig{
		Enabled:    true,
		BaseDir:    r.baseDir,
		Generation: generation,
		Relay:      relay,
	})
	if err != nil {
		return err
	}
	r.listener = listener
	return nil
}

func (r *legacyRelayRuntime) close() error {
	if r == nil || r.listener == nil {
		return nil
	}
	return r.listener.Close()
}

func canonicalExecutablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return canonicalPath(executable)
}

func legacyRelayRuntimeBaseDir() (string, error) {
	if !config.HAP01BRelayEnabled() {
		return "", nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		return "", errors.New("HAP-01B per-user runtime directory is unavailable")
	}
	return filepath.Join(cacheDir, "engram", "run", "hap-01b"), nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}
