package serverevents

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/registry"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// ---------------------------------------------------------------------------
// Fake module infrastructure
// ---------------------------------------------------------------------------

// fakeModule is a minimal EngramModule + ProjectRemovalAware that records
// OnProjectRemoved calls.
type fakeModule struct {
	name     string
	removals chan string
}

func newFakeModule(name string) *fakeModule {
	return &fakeModule{name: name, removals: make(chan string, 32)}
}

func (f *fakeModule) Name() string                                      { return f.name }
func (f *fakeModule) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (f *fakeModule) Shutdown(_ context.Context) error                  { return nil }
func (f *fakeModule) OnProjectRemoved(projectID string) {
	select {
	case f.removals <- projectID:
	default: // buffer full — test will time out on receive
	}
}

// panicModule is a ProjectRemovalAware that always panics in
// OnProjectRemoved. Used to verify that fanOutRemoval isolates panics per
// handler so one crashy module cannot block fan-out to the rest.
type panicModule struct {
	name string
}

func (p *panicModule) Name() string                                      { return p.name }
func (p *panicModule) Init(_ context.Context, _ module.ModuleDeps) error { return nil }
func (p *panicModule) Shutdown(_ context.Context) error                  { return nil }
func (p *panicModule) OnProjectRemoved(_ string) {
	panic("panicModule: simulated handler crash")
}

// buildRegistry registers the given modules and freezes the registry.
func buildRegistry(mods ...module.EngramModule) *registry.Registry {
	r := registry.New()
	for _, m := range mods {
		if err := r.Register(m); err != nil {
			panic(err)
		}
	}
	r.Freeze()
	return r
}

// fakeTracker is an in-memory ProjectTracker for test injection. It returns
// a fixed snapshot of project IDs. Tests that don't care about the tracked
// set can use newFakeTracker() with no args for an empty snapshot.
type fakeTracker struct {
	ids []string
}

func newFakeTracker(ids ...string) *fakeTracker {
	return &fakeTracker{ids: ids}
}

func (t *fakeTracker) ConnectedProjectIDs() []string {
	if t == nil {
		return nil
	}
	out := make([]string, len(t.ids))
	copy(out, t.ids)
	return out
}

// ---------------------------------------------------------------------------
// Fake gRPC server
// ---------------------------------------------------------------------------

// fakeEngramServer is a minimal EngramServiceServer that provides
// ProjectEvents streaming and SyncProjectState for bridge tests.
type fakeEngramServer struct {
	pb.UnimplementedEngramServiceServer

	// eventCh allows tests to push events to connected stream clients.
	eventCh chan *pb.ProjectEvent
	// syncResp is the non-strict response returned by SyncProjectState.
	// Ignored when syncStrict is true.
	syncResp *pb.SyncProjectStateResponse
	// syncCalled counts SyncProjectState invocations.
	syncCalled atomic.Int32
	// drop, when true, causes the next stream send to return an error
	// (simulates server-side drop for reconnect tests).
	drop atomic.Bool

	// syncStrict, when true, enables intersection semantics in SyncProjectState:
	// only IDs present in BOTH the request's local_project_ids AND the
	// serverRemoved set are returned as removed. This matches the real
	// engram-server implementation of FR-6. When false, syncResp is returned
	// verbatim (legacy/dedup test behaviour).
	syncStrict bool
	// serverRemoved is the set of project IDs the server considers removed.
	// Used only in strict mode.
	serverRemoved map[string]struct{}

	// syncReceivedMu protects syncReceived.
	syncReceivedMu sync.Mutex
	// syncReceived records every project ID ever sent in a SyncProjectState
	// request's local_project_ids field. Tests use syncSawProject() to assert
	// the bridge actually queried its ProjectTracker.
	syncReceived map[string]struct{}
}

func newFakeServer() *fakeEngramServer {
	return &fakeEngramServer{
		eventCh:      make(chan *pb.ProjectEvent, 32),
		syncResp:     &pb.SyncProjectStateResponse{},
		syncReceived: make(map[string]struct{}),
	}
}

// syncSawProject reports whether the server ever received a SyncProjectState
// request containing the given project ID in local_project_ids. Used by
// regression tests to assert the bridge correctly queried its ProjectTracker.
func (s *fakeEngramServer) syncSawProject(id string) bool {
	s.syncReceivedMu.Lock()
	defer s.syncReceivedMu.Unlock()
	_, ok := s.syncReceived[id]
	return ok
}

func (s *fakeEngramServer) ProjectEvents(req *pb.ProjectEventsRequest, stream pb.EngramService_ProjectEventsServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev, ok := <-s.eventCh:
			if !ok {
				return nil
			}
			if s.drop.Load() {
				return status.Error(codes.Unavailable, "simulated drop")
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func (s *fakeEngramServer) SyncProjectState(_ context.Context, req *pb.SyncProjectStateRequest) (*pb.SyncProjectStateResponse, error) {
	s.syncCalled.Add(1)

	// Record every incoming ID so tests can assert the bridge queried its
	// ProjectTracker correctly.
	s.syncReceivedMu.Lock()
	for _, id := range req.GetLocalProjectIds() {
		s.syncReceived[id] = struct{}{}
	}
	s.syncReceivedMu.Unlock()

	if !s.syncStrict {
		// Legacy/dedup test mode: return the pre-configured response verbatim.
		return s.syncResp, nil
	}

	// Strict mode: intersect request IDs against serverRemoved. This matches
	// the real engram-server FR-6 semantics: only IDs that are both in the
	// client's local set AND in the server's removed set are returned.
	resp := &pb.SyncProjectStateResponse{}
	for _, id := range req.GetLocalProjectIds() {
		if _, removed := s.serverRemoved[id]; removed {
			resp.Removed = append(resp.Removed, id)
		}
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// bufconn helpers
// ---------------------------------------------------------------------------

const testBufSize = 1 << 20 // 1 MiB

// startFakeServer starts an in-process gRPC server backed by bufconn.
// Returns the server struct, an EventsClient connected over the in-process
// network, and a cleanup function.
func startFakeServer(t *testing.T, srv *fakeEngramServer) (EventsClient, func()) {
	t.Helper()

	lis := bufconn.Listen(testBufSize)
	gs := grpc.NewServer()
	pb.RegisterEngramServiceServer(gs, srv)

	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}

	client := newGRPCEventsClient(conn)
	cleanup := func() {
		conn.Close()
		gs.Stop()
		lis.Close()
	}
	return client, cleanup
}

// testLogger returns a true discard logger for test isolation — parallel
// tests would otherwise interleave debug output on stderr.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestBridge_HappyPath_StreamEvent verifies that a PROJECT_EVENT_TYPE_REMOVED
// event delivered on the stream triggers OnProjectRemoved within 1 s.
func TestBridge_HappyPath_StreamEvent(t *testing.T) {
	t.Parallel()

	srv := newFakeServer()
	mod := newFakeModule("loom")
	reg := buildRegistry(mod)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	bridge := NewBridge(logger, reg, newFakeTracker(), client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	// Push a removal event to the server.
	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-1",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-happy",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	select {
	case pid := <-mod.removals:
		if pid != "proj-happy" {
			t.Errorf("expected proj-happy, got %s", pid)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("OnProjectRemoved not fired within 1 s")
	}

	bridge.Stop()
}

// TestBridge_Reconnect verifies that after the server drops the stream, the
// bridge backs off and reconnects. The reconnect window test uses a short
// backoff by resending an event after the drop.
func TestBridge_Reconnect(t *testing.T) {
	t.Parallel()

	srv := newFakeServer()
	mod := newFakeModule("loom")
	reg := buildRegistry(mod)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	bridge := NewBridge(logger, reg, newFakeTracker(), client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	// First event — delivered normally.
	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-before-drop",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-before-drop",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	select {
	case pid := <-mod.removals:
		if pid != "proj-before-drop" {
			t.Errorf("first event: expected proj-before-drop, got %s", pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event not delivered")
	}

	// Simulate drop: next send returns Unavailable.
	srv.drop.Store(true)
	// Trigger the drop by sending an event.
	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-drop-trigger",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-will-drop",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	// Wait briefly for the stream to notice the error and start reconnecting.
	time.Sleep(100 * time.Millisecond)

	// Re-enable the server.
	srv.drop.Store(false)

	// Send a new event — bridge should reconnect and deliver it within backoffMin + slack.
	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-after-reconnect",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-after-reconnect",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	select {
	case pid := <-mod.removals:
		// May get proj-will-drop or proj-after-reconnect depending on timing.
		t.Logf("got removal: %s", pid)
		// Drain any remaining buffered events.
	drain:
		for {
			select {
			case extra := <-mod.removals:
				t.Logf("got additional removal: %s", extra)
			default:
				break drain
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not reconnect and deliver event within 5 s")
	}

	bridge.Stop()
}

// TestBridge_DedupAcrossStreamAndHeartbeat verifies that the same project
// removal arriving via both the stream and the heartbeat fires OnProjectRemoved
// exactly once.
func TestBridge_DedupAcrossStreamAndHeartbeat(t *testing.T) {
	t.Parallel()

	srv := newFakeServer()
	// Prime the heartbeat to report "proj-dedup" as removed.
	srv.syncResp = &pb.SyncProjectStateResponse{
		Removed: []string{"proj-dedup"},
	}

	mod := newFakeModule("loom")
	reg := buildRegistry(mod)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	bridge := NewBridge(logger, reg, newFakeTracker(), client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	// Stream event first.
	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-dedup",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-dedup",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	// Wait for the stream event to be processed.
	select {
	case pid := <-mod.removals:
		if pid != "proj-dedup" {
			t.Errorf("expected proj-dedup from stream, got %s", pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream event not delivered")
	}

	// Manually trigger a sync (bypassing the 60 s ticker).
	bridge.syncProjectState(ctx)

	// The heartbeat response also contains proj-dedup — should be deduplicated.
	select {
	case extra := <-mod.removals:
		t.Errorf("dedup failed: got second OnProjectRemoved for %s", extra)
	case <-time.After(200 * time.Millisecond):
		// No second call — dedup worked correctly.
	}

	bridge.Stop()
}

// TestBridge_HeartbeatCatchesMissed verifies the full heartbeat safety net:
// the bridge's fakeTracker is seeded with a live project ID, the fake server
// is configured to intersect that ID against the incoming local_project_ids
// list (matching real SyncProjectState semantics), and the bridge should fan
// out OnProjectRemoved for the intersection. This test is the regression
// guard for the CRIT finding on PR #171: previously the bridge tracked
// projects only from REMOVED stream events, so heartbeat calls went out with
// an empty local_project_ids and could never catch anything. The fix was to
// have the bridge query dispatcher.ConnectedProjectIDs() on each tick; this
// test proves the end-to-end contract works with a properly-behaving server.
func TestBridge_HeartbeatCatchesMissed(t *testing.T) {
	t.Parallel()

	// Use a strict fake server that only reports IDs the bridge sent.
	srv := newFakeServer()
	srv.syncStrict = true
	// Pre-mark "proj-missed" as removed on the server side.
	srv.serverRemoved = map[string]struct{}{"proj-missed": {}}

	mod := newFakeModule("loom")
	reg := buildRegistry(mod)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	// Seed the bridge tracker with the live project — this is what the real
	// dispatcher would have after OnProjectConnect("proj-missed").
	tracker := newFakeTracker("proj-missed")
	bridge := NewBridge(logger, reg, tracker, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Do not start the background goroutines — call syncProjectState directly
	// to simulate a heartbeat tick without waiting 60 s.
	bridge.client = client
	bridge.syncProjectState(ctx)

	select {
	case pid := <-mod.removals:
		if pid != "proj-missed" {
			t.Errorf("expected proj-missed, got %s", pid)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("heartbeat did not fan out OnProjectRemoved")
	}

	// Sanity: the fake server should have recorded the request and confirmed
	// that proj-missed was actually sent in local_project_ids.
	if srv.syncCalled.Load() != 1 {
		t.Errorf("expected exactly 1 SyncProjectState call, got %d", srv.syncCalled.Load())
	}
	if !srv.syncSawProject("proj-missed") {
		t.Error("server never received proj-missed in local_project_ids — bridge did not query tracker")
	}
}

// TestBridge_FanOutRemoval_PanicIsolation verifies FR-15 semantics for the
// bridge: if one ProjectRemovalAware module panics in OnProjectRemoved,
// the bridge must recover + log and continue to fan out to the remaining
// modules registered after the panicking one.
func TestBridge_FanOutRemoval_PanicIsolation(t *testing.T) {
	t.Parallel()

	srv := newFakeServer()
	panicker := &panicModule{name: "panicker"}
	survivor := newFakeModule("survivor")
	// Registration order matters: panicker must come BEFORE survivor so
	// that fan-out reaches it first. If the bridge did not recover the
	// panic, survivor would never see the event.
	reg := buildRegistry(panicker, survivor)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	bridge := NewBridge(logger, reg, newFakeTracker(), client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	srv.eventCh <- &pb.ProjectEvent{
		EventId:         "evt-panic-isolation",
		EventType:       pb.ProjectEventType_PROJECT_EVENT_TYPE_REMOVED,
		ProjectId:       "proj-panic-ok",
		TimestampUnixMs: time.Now().UnixMilli(),
	}

	select {
	case pid := <-survivor.removals:
		if pid != "proj-panic-ok" {
			t.Errorf("expected proj-panic-ok, got %s", pid)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("survivor module did not receive OnProjectRemoved after panic — recovery missing")
	}

	bridge.Stop()
}

// TestBridge_StopExitsCleanly verifies that Stop() unblocks within 5 s (NFR-9
// budget) after Start().
func TestBridge_StopExitsCleanly(t *testing.T) {
	t.Parallel()

	srv := newFakeServer()
	mod := newFakeModule("loom")
	reg := buildRegistry(mod)

	client, cleanup := startFakeServer(t, srv)
	defer cleanup()

	logger := testLogger()
	bridge := NewBridge(logger, reg, newFakeTracker(), client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge.Start(ctx)

	done := make(chan struct{})
	go func() {
		bridge.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("Bridge.Stop() did not return within 5 s (NFR-9 budget)")
	}
}
