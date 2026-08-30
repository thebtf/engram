package legacyrelay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/thebtf/mcp-mux/muxcore/ipc"
)

func TestStartListenerDisabledHasNoFilesystemEffect(t *testing.T) {
	temp := t.TempDir()
	listener, err := StartListener(context.Background(), ListenerConfig{Enabled: false, BaseDir: temp})
	if err != nil {
		t.Fatalf("StartListener disabled: %v", err)
	}
	if listener.Endpoint() != "" || listener.LocatorPath() != "" {
		t.Fatalf("disabled listener exposed paths: endpoint=%q locator=%q", listener.Endpoint(), listener.LocatorPath())
	}
	if _, err := os.Stat(RuntimeLocatorPath(temp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled listener locator stat error = %v, want not exist", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close disabled listener: %v", err)
	}
}

func TestStartListenerPublishesAndRemovesOwnedLocator(t *testing.T) {
	temp := t.TempDir()
	generation, relay := newTestListenerRelay(t)
	listener, err := StartListener(context.Background(), ListenerConfig{
		Enabled:       true,
		BaseDir:       temp,
		Generation:    generation,
		Relay:         relay,
		MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	locator, err := ReadLocator(listener.LocatorPath())
	if err != nil {
		t.Fatalf("ReadLocator: %v", err)
	}
	if locator.Endpoint != listener.Endpoint() || locator.Generation != generation || locator.Protocol != Protocol {
		t.Fatalf("locator = %#v", locator)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}
	select {
	case <-listener.Done():
	default:
		t.Fatal("listener Done is not closed")
	}
	if _, err := os.Stat(RuntimeLocatorPath(temp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed listener locator stat error = %v, want not exist", err)
	}
}

func TestStartListenerStopsOnParentCancellation(t *testing.T) {
	temp := t.TempDir()
	generation, relay := newTestListenerRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := StartListener(ctx, ListenerConfig{Enabled: true, BaseDir: temp, Generation: generation, Relay: relay})
	if err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	cancel()
	select {
	case <-listener.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not stop after parent cancellation")
	}
	if _, err := os.Stat(RuntimeLocatorPath(temp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled listener locator stat error = %v, want not exist", err)
	}
}

func TestListenerRealIPCFailsClosedWithoutBoundChild(t *testing.T) {
	temp := t.TempDir()
	generation := mustGeneration(t, "daemon-generation")
	inspector := NewOSProcessInspector()
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	capabilities, err := NewCapabilityRegistry(CapabilityRegistryConfig{TTL: time.Minute, Now: time.Now, Inspector: inspector, Children: children})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	relay, err := NewRelay(RelayConfig{
		Generation: generation, AdapterGate: ExactAdapterGate(mustAdapter(t)),
		Bootstrapper: NewBootstrapper(children, inspector, 4), Capabilities: capabilities,
		PeerResolver: NewOSPeerResolver(), Now: time.Now, SafetyDeadline: time.Second,
		MaxFrameBytes: DefaultMaxFrameBytes, CapabilityRoutes: mustRouteSet(t, RouteSessionStartContext, RouteAmbientCandidates),
	})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	listener, err := StartListener(context.Background(), ListenerConfig{Enabled: true, BaseDir: temp, Generation: generation, Relay: relay})
	if err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	defer listener.Close()
	connection, err := ipc.DialTimeout(listener.Endpoint(), time.Second)
	if err != nil {
		t.Fatalf("DialTimeout: %v", err)
	}
	if _, err := fmt.Fprintf(connection, "%s\n", testIdentityFrame(time.Now().Add(time.Second).UnixMilli(), "")); err != nil {
		t.Fatalf("write identity frame: %v", err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read relay response: %v", err)
	}
	_ = connection.Close()
	var response struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode relay response: %v", err)
	}
	if response.Kind != "NO_DELIVERY" || response.Reason != "BOOTSTRAP_UNAVAILABLE" {
		t.Fatalf("relay response = %#v", response)
	}
}

func newTestListenerRelay(t *testing.T) (DaemonGeneration, *Relay) {
	t.Helper()
	generation := mustGeneration(t, "daemon-generation")
	inspector := &fakeProcessInspector{processes: map[int]ProcessIdentity{}}
	children := NewChildRegistry(inspector, ChildImageGateFunc(func(ProcessImage) bool { return true }))
	capabilities, err := NewCapabilityRegistry(CapabilityRegistryConfig{TTL: time.Minute, Now: time.Now, Inspector: inspector, Children: children})
	if err != nil {
		t.Fatalf("NewCapabilityRegistry: %v", err)
	}
	relay, err := NewRelay(RelayConfig{
		Generation:       generation,
		AdapterGate:      ExactAdapterGate(mustAdapter(t)),
		Bootstrapper:     NewBootstrapper(children, inspector, 4),
		Capabilities:     capabilities,
		PeerResolver:     PeerResolverFunc(func(_ net.Conn) (int, error) { return 0, errors.New("unused") }),
		Now:              time.Now,
		SafetyDeadline:   time.Second,
		MaxFrameBytes:    DefaultMaxFrameBytes,
		CapabilityRoutes: mustRouteSet(t, RouteSessionStartContext, RouteAmbientCandidates),
	})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	return generation, relay
}
