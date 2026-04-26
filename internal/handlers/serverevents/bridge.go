package serverevents

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/registry"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

const (
	// backoffMin is the initial reconnect wait after a stream error.
	backoffMin = 1 * time.Second
	// backoffMax is the maximum reconnect wait (NFR-3: reconnect within 30 s
	// under normal conditions; 60 s is the hard cap).
	backoffMax = 60 * time.Second

	// heartbeatBase is the base period for SyncProjectState ticks (NFR-4).
	heartbeatBase = 60 * time.Second
	// heartbeatJitter is the maximum random jitter added to each tick.
	// A random value in [0, heartbeatJitter) is added to heartbeatBase so the
	// effective period is 60 s ± 5 s as required by NFR-4.
	heartbeatJitter = 10 * time.Second
)

// ProjectTracker is the read-only accessor for the set of currently-connected
// project IDs on the daemon. The dispatcher.Dispatcher satisfies this
// interface natively — its OnProjectConnect/Disconnect callbacks maintain
// the underlying sync.Map. Tests inject a fake tracker to seed arbitrary
// project sets.
//
// The bridge calls ConnectedProjectIDs on every heartbeat tick to build the
// SyncProjectState request. Returning an empty slice is valid (no sessions).
type ProjectTracker interface {
	ConnectedProjectIDs() []string
}

// Bridge consumes engram-server's ProjectEvents stream and fans out
// OnProjectRemoved to all ProjectRemovalAware modules in the daemon.
//
// Two concurrent paths provide at-least-once delivery:
//  1. runEventStream — persistent gRPC server-streaming RPC with reconnect.
//  2. runSyncTicker  — 60s ± 5s heartbeat via SyncProjectState.
//
// Events from both paths are deduplicated via an in-memory LRU so that
// OnProjectRemoved fires exactly once per project per removal.
//
// Tracked-project source: **Option A (revised 2026-04-15 for CRIT fix)** —
// the bridge queries dispatcher.ConnectedProjectIDs() on every heartbeat to
// get the authoritative local project set. The dispatcher already tracks
// sessions via OnProjectConnect/Disconnect (added in Phase 5); exposing a
// read-only accessor is a minimal framework change and lets the heartbeat
// genuinely catch events missed during a stream drop.
//
// A previous implementation seeded a bridge-local sync.Map from
// ProjectEvents stream messages, but that had a correctness bug: projects
// that were only ever CONNECTED (never REMOVED) never entered the local
// set, so the heartbeat could not report them to the server for
// reconciliation. This was caught in PR #171 review.
type Bridge struct {
	clientID  string // "${pid}-${startUnix}" per proto-extensions.md
	token     string // ENGRAM_TOKEN (per-workstation keycard); empty = no auth
	serverURL string // ENGRAM_SERVER_URL
	logger    *slog.Logger
	reg       *registry.Registry
	tracker   ProjectTracker // live project IDs from the dispatcher (or fake in tests)
	dedup     *lru
	client    EventsClient     // injectable for tests
	conn      *grpc.ClientConn // owned gRPC conn (nil when client is injected for tests)

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBridge creates a new Bridge.
//
// serverURL and token are read from the environment (ENGRAM_SERVER_URL /
// ENGRAM_AUTH_ADMIN_TOKEN). If serverURL is empty, Start is a no-op — the bridge
// will log a warning and return without starting any goroutines.
//
// The logger should be scoped to the caller; the bridge prefixes its own
// log entries with component="serverevents-bridge".
//
// The tracker parameter exposes the daemon's set of currently-connected
// project IDs for the heartbeat path. In production it's the dispatcher
// (which implements ConnectedProjectIDs natively). In tests inject a fake.
//
// The client parameter is used for test injection; pass nil for production
// (the bridge will dial its own gRPC connection using serverURL + token).
func NewBridge(logger *slog.Logger, reg *registry.Registry, tracker ProjectTracker, client EventsClient) *Bridge {
	pid := os.Getpid()
	startUnix := time.Now().Unix()
	clientID := fmt.Sprintf("%d-%d", pid, startUnix)

	// Precedence aligned with internal/config/envnames.go: EnvServerURL
	// ("ENGRAM_URL") is canonical, EnvServerURLAlt ("ENGRAM_SERVER_URL")
	// is the deprecated fallback. Reading them in the opposite order would
	// let a divergent migration deploy serve tools/call and the
	// serverevents bridge from two different backends within the same
	// daemon process.
	serverURL := os.Getenv(config.EnvServerURL)
	if serverURL == "" {
		if fallback := os.Getenv(config.EnvServerURLAlt); fallback != "" {
			serverURL = fallback
			logger.Warn("ENGRAM_SERVER_URL is deprecated for bridge configuration; please use ENGRAM_URL instead")
		}
	}
	// FR-3 / Plan ADR-003: workstation reads the client-semantic env var.
	// The pre-v6 ENGRAM_AUTH_ADMIN_TOKEN is intentionally NOT consulted here
	// (FR-5 — no legacy fallback chains).
	token := os.Getenv(config.EnvWorkstationToken)

	return &Bridge{
		clientID:  clientID,
		token:     token,
		serverURL: serverURL,
		logger:    logger.With("component", "serverevents-bridge", "client_id", clientID),
		reg:       reg,
		tracker:   tracker,
		dedup:     newLRU(dedupCapacity),
		client:    client,
	}
}

// Start launches the runEventStream and runSyncTicker goroutines under the
// given parent context. It is safe to call exactly once. If the bridge has
// no server URL configured AND no test client was injected, it logs a
// warning and returns immediately (production no-op path).
//
// The bridge does NOT own the parent context — the caller (main.go) owns the
// daemon context and passes it in. Stop() cancels the bridge's internal
// derived context.
func (b *Bridge) Start(ctx context.Context) {
	// A test-injected client bypasses the serverURL check: tests create a
	// bufconn-backed EventsClient and never set ENGRAM_SERVER_URL. Only the
	// production path (no injected client) requires a configured URL.
	if b.client == nil {
		if b.serverURL == "" {
			b.logger.Warn("serverevents bridge disabled: ENGRAM_SERVER_URL not set")
			return
		}
		conn, err := b.dialGRPC()
		if err != nil {
			b.logger.Error("serverevents bridge: failed to dial gRPC", "error", err)
			return
		}
		b.conn = conn
		b.client = newGRPCEventsClient(conn)
	}

	innerCtx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	b.logger.Info("serverevents bridge starting",
		"server_url", safeURL(b.serverURL),
	)

	b.wg.Add(2)
	go func() {
		defer b.wg.Done()
		b.runEventStream(innerCtx)
	}()
	go func() {
		defer b.wg.Done()
		b.runSyncTicker(innerCtx)
	}()
}

// Stop cancels the bridge's internal context and waits for both goroutines to
// exit. Stop is idempotent — calling it before Start, or calling it multiple
// times, is safe.
func (b *Bridge) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
	// Close the owned gRPC connection (only set when the bridge dialled itself).
	// Test-injected clients manage their own connection lifetime.
	if b.conn != nil {
		_ = b.conn.Close()
	}
	b.logger.Info("serverevents bridge stopped")
}

// ---------------------------------------------------------------------------
// runEventStream — real-time gRPC stream with exponential backoff (T042)
// ---------------------------------------------------------------------------

// runEventStream maintains a persistent ProjectEvents gRPC stream.
// On recv error it restarts the stream with exponential backoff.
// On context cancellation it exits cleanly.
//
// Backoff is reset to backoffMin after every successful stream open so that
// a momentary disconnect after a long stable period does not inherit the
// large backoff left over from an earlier reconnect storm.
func (b *Bridge) runEventStream(ctx context.Context) {
	backoff := backoffMin

	for {
		if ctx.Err() != nil {
			return
		}

		opened, err := b.consumeStream(ctx)
		if opened {
			// Fresh connection succeeded — any earlier inflation of the
			// backoff window is no longer relevant.
			backoff = backoffMin
		}
		if ctx.Err() != nil {
			// Context cancelled — clean shutdown, not an error.
			return
		}

		b.logger.Warn("serverevents stream disconnected; reconnecting",
			"error", err,
			"backoff", backoff,
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Exponential backoff, capped at backoffMax.
		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

// consumeStream opens a single ProjectEvents stream and reads events until
// an error occurs or ctx is cancelled. Returns (opened, err):
//   - opened reports whether the stream RPC itself succeeded at least once
//     (used by runEventStream to reset the backoff window).
//   - err carries the first stream failure or nil on clean ctx cancellation.
func (b *Bridge) consumeStream(ctx context.Context) (bool, error) {
	callCtx := b.outgoingContext(ctx)

	stream, err := b.client.ProjectEvents(callCtx, &pb.ProjectEventsRequest{
		ClientId: b.clientID,
	})
	if err != nil {
		return false, fmt.Errorf("open ProjectEvents stream: %w", err)
	}

	b.logger.Info("serverevents stream connected")
	opened := true

	for {
		ev, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return opened, nil
			}
			return opened, fmt.Errorf("recv: %w", err)
		}

		b.handleEvent(ev)
	}
}

// handleEvent processes a single ProjectEvent, applying dedup and fan-out.
func (b *Bridge) handleEvent(ev *pb.ProjectEvent) {
	if ev.GetEventType() != pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED {
		return
	}

	projectID := ev.GetProjectId()
	if projectID == "" {
		return
	}

	// Use a stable, lowercase event type name for dedup so that stream events
	// (which carry the proto enum) and heartbeat events (which use the literal
	// string "removed") share the same key space.
	eventTypeName := "removed"
	if b.dedup.Mark(eventTypeName, projectID) {
		b.logger.Debug("serverevents bridge: dedup suppressed duplicate event",
			"project_id", projectID,
			"event_type", eventTypeName,
		)
		return
	}

	b.logger.Info("serverevents bridge: project removed (stream)",
		"project_id", projectID,
		"event_id", ev.GetEventId(),
	)
	b.fanOutRemoval(projectID)
}

// ---------------------------------------------------------------------------
// runSyncTicker — 60 s ± 5 s heartbeat via SyncProjectState (T043)
// ---------------------------------------------------------------------------

// runSyncTicker fires a SyncProjectState call every 60 s ± 5 s.
// It fans out OnProjectRemoved for any project the server marks as removed.
func (b *Bridge) runSyncTicker(ctx context.Context) {
	for {
		// Compute jitter: random offset in [0, heartbeatJitter).
		jitter := time.Duration(rand.Int63n(int64(heartbeatJitter)))
		wait := heartbeatBase + jitter - (heartbeatJitter / 2) // centred: ±5 s
		// Clamp: never go below heartbeatBase - heartbeatJitter/2
		if wait < heartbeatBase-heartbeatJitter/2 {
			wait = heartbeatBase - heartbeatJitter/2
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		b.syncProjectState(ctx)
	}
}

// syncProjectState performs one SyncProjectState call and fans out removals.
func (b *Bridge) syncProjectState(ctx context.Context) {
	localIDs := b.localProjectIDs()

	callCtx, cancel := context.WithTimeout(b.outgoingContext(ctx), 10*time.Second)
	defer cancel()

	resp, err := b.client.SyncProjectState(callCtx, &pb.SyncProjectStateRequest{
		ClientId:        b.clientID,
		LocalProjectIds: localIDs,
	})
	if err != nil {
		if ctx.Err() != nil {
			return // clean shutdown
		}
		b.logger.Warn("serverevents bridge: SyncProjectState failed", "error", err)
		return
	}

	if len(resp.GetUnknown()) > 0 {
		b.logger.Warn("serverevents bridge: server reports unknown project IDs",
			"count", len(resp.GetUnknown()),
		)
	}

	for _, projectID := range resp.GetRemoved() {
		// Mirror the guard in handleEvent — a malformed or partial server
		// response that includes an empty ID must not fan out
		// OnProjectRemoved("") to every module.
		if projectID == "" {
			continue
		}
		if b.dedup.Mark("removed", projectID) {
			b.logger.Debug("serverevents bridge: heartbeat dedup suppressed",
				"project_id", projectID,
			)
			continue
		}

		b.logger.Info("serverevents bridge: project removed (heartbeat)",
			"project_id", projectID,
		)
		b.fanOutRemoval(projectID)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fanOutRemoval calls OnProjectRemoved on every ProjectRemovalAware module
// in registration order. Each handler runs under defer+recover so that a
// panic inside one module cannot crash the bridge or interrupt fan-out to
// the remaining modules. This matches the panic-isolation discipline the
// dispatcher applies to lifecycle callbacks (FR-15).
func (b *Bridge) fanOutRemoval(projectID string) {
	b.reg.ForEachProjectRemovalAware(func(h module.ProjectRemovalAware) {
		defer func() {
			if r := recover(); r != nil {
				b.logger.Error("serverevents bridge: removal handler panicked",
					"project_id", projectID,
					"handler", fmt.Sprintf("%T", h),
					"panic", r,
				)
			}
		}()
		h.OnProjectRemoved(projectID)
	})
}

// localProjectIDs returns a snapshot of all project IDs the daemon currently
// has active sessions for, via the injected ProjectTracker. In production
// this is dispatcher.ConnectedProjectIDs(), which is populated by
// OnProjectConnect / OnProjectDisconnect callbacks. Returns an empty slice
// if no sessions are active OR if the tracker is nil (defensive).
func (b *Bridge) localProjectIDs() []string {
	if b.tracker == nil {
		return nil
	}
	return b.tracker.ConnectedProjectIDs()
}

// outgoingContext returns a derived context with the Bearer token attached
// as outgoing gRPC metadata. The token is required for both unary and
// streaming RPCs because the server-side interceptor covers unary only;
// the streaming handler reads metadata directly.
func (b *Bridge) outgoingContext(ctx context.Context) context.Context {
	if b.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+b.token)
}

// dialGRPC creates the bridge's own gRPC connection.
// The bridge does not share engramcore's pooled connection because its
// lifetime is independent (persistent stream vs. per-session calls) and
// adding a StreamInterceptor to the shared pool would change its behaviour.
func (b *Bridge) dialGRPC() (*grpc.ClientConn, error) {
	addr, err := parseGRPCAddr(b.serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(4 << 20), // 4 MB — events are small
		),
	}

	tlsCA := os.Getenv("ENGRAM_TLS_CA")
	switch {
	case tlsCA != "":
		creds, err := credentials.NewClientTLSFromFile(tlsCA, "")
		if err != nil {
			return nil, fmt.Errorf("load TLS CA: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	case isHTTPS(b.serverURL):
		creds := credentials.NewClientTLSFromCert(nil, "")
		opts = append(opts, grpc.WithTransportCredentials(creds))
	default:
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.NewClient(addr, opts...)
}
