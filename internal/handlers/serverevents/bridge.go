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

// Bridge consumes engram-server's ProjectEvents stream and fans out
// OnProjectRemoved to all ProjectRemovalAware modules in the daemon.
//
// Two concurrent paths provide at-least-once delivery:
//   1. runEventStream — persistent gRPC server-streaming RPC with reconnect.
//   2. runSyncTicker  — 60s ± 5s heartbeat via SyncProjectState.
//
// Events from both paths are deduplicated via an in-memory LRU so that
// OnProjectRemoved fires exactly once per project per removal.
//
// Tracked-project option choice: **Option C** — the bridge maintains its
// own sync.Map seeded by incoming ProjectEvents. It does NOT hook into the
// dispatcher's OnProjectConnect/Disconnect because:
//   a) The dispatcher does not expose connection hooks that the bridge can
//      subscribe to (only module.ProjectLifecycle callbacks are available,
//      which would require the bridge to be a full EngramModule).
//   b) The loom module's tracked projects already drive cancellation via
//      OnProjectRemoved; the bridge's SyncProjectState call is a safety net
//      for missed events, not a primary source of truth.
//   c) Starting with an empty local set is safe: the first ProjectEvents
//      message seeds the tracker; the heartbeat catches everything after.
type Bridge struct {
	clientID    string // "${pid}-${startUnix}" per proto-extensions.md
	token       string // ENGRAM_API_TOKEN; empty = no auth
	serverURL   string // ENGRAM_SERVER_URL
	logger      *slog.Logger
	reg         *registry.Registry
	dedup       *lru
	tracked     sync.Map  // projectID (string) → struct{} — seeded by stream events
	client      EventsClient // injectable for tests

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewBridge creates a new Bridge.
//
// serverURL and token are read from the environment (ENGRAM_SERVER_URL /
// ENGRAM_API_TOKEN). If serverURL is empty, Start is a no-op — the bridge
// will log a warning and return without starting any goroutines.
//
// The logger should be scoped to the caller; the bridge prefixes its own
// log entries with component="serverevents-bridge".
//
// The client parameter is used for test injection; pass nil for production
// (the bridge will dial its own gRPC connection using serverURL + token).
func NewBridge(logger *slog.Logger, reg *registry.Registry, client EventsClient) *Bridge {
	pid := os.Getpid()
	startUnix := time.Now().Unix()
	clientID := fmt.Sprintf("%d-%d", pid, startUnix)

	serverURL := os.Getenv("ENGRAM_SERVER_URL")
	if serverURL == "" {
		serverURL = os.Getenv("ENGRAM_URL") // legacy env var
	}
	token := os.Getenv("ENGRAM_API_TOKEN")

	return &Bridge{
		clientID:  clientID,
		token:     token,
		serverURL: serverURL,
		logger:    logger.With("component", "serverevents-bridge", "client_id", clientID),
		reg:       reg,
		dedup:     newLRU(dedupCapacity),
		client:    client,
	}
}

// Start launches the runEventStream and runSyncTicker goroutines under the
// given parent context. It is safe to call exactly once. If the bridge has
// no server URL configured it logs a warning and returns immediately.
//
// The bridge does NOT own the parent context — the caller (main.go) owns the
// daemon context and passes it in. Stop() cancels the bridge's internal
// derived context.
func (b *Bridge) Start(ctx context.Context) {
	if b.serverURL == "" {
		b.logger.Warn("serverevents bridge disabled: ENGRAM_SERVER_URL not set")
		return
	}

	// If no client was injected (production path), dial our own connection.
	if b.client == nil {
		conn, err := b.dialGRPC()
		if err != nil {
			b.logger.Error("serverevents bridge: failed to dial gRPC", "error", err)
			return
		}
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
	b.logger.Info("serverevents bridge stopped")
}

// ---------------------------------------------------------------------------
// runEventStream — real-time gRPC stream with exponential backoff (T042)
// ---------------------------------------------------------------------------

// runEventStream maintains a persistent ProjectEvents gRPC stream.
// On recv error it restarts the stream with exponential backoff.
// On context cancellation it exits cleanly.
func (b *Bridge) runEventStream(ctx context.Context) {
	backoff := backoffMin

	for {
		if ctx.Err() != nil {
			return
		}

		err := b.consumeStream(ctx)
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
// an error occurs or ctx is cancelled. Returns the first error (or nil on
// clean ctx cancellation).
func (b *Bridge) consumeStream(ctx context.Context) error {
	callCtx := b.outgoingContext(ctx)

	stream, err := b.client.ProjectEvents(callCtx, &pb.ProjectEventsRequest{
		ClientId: b.clientID,
	})
	if err != nil {
		return fmt.Errorf("open ProjectEvents stream: %w", err)
	}

	b.logger.Info("serverevents stream connected")

	for {
		ev, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
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

	// Seed the local tracker so SyncProjectState heartbeat knows about this project.
	b.tracked.Store(projectID, struct{}{})

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
		ClientId:       b.clientID,
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
// in registration order.
func (b *Bridge) fanOutRemoval(projectID string) {
	b.reg.ForEachProjectRemovalAware(func(h module.ProjectRemovalAware) {
		h.OnProjectRemoved(projectID)
	})
}

// localProjectIDs returns a snapshot of all project IDs the bridge currently
// tracks (seeded by incoming stream events).
func (b *Bridge) localProjectIDs() []string {
	var ids []string
	b.tracked.Range(func(k, _ any) bool {
		if id, ok := k.(string); ok {
			ids = append(ids, id)
		}
		return true
	})
	return ids
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
