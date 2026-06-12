// Package sse provides Server-Sent Events broadcasting for engram.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// recWriter records bytes written and implements http.Flusher.
// Used in place of a real ResponseWriter for unit tests.
type recWriter struct {
	mu     sync.Mutex
	hdr    http.Header
	status int
	buf    []byte
}

func newRecWriter() *recWriter {
	return &recWriter{hdr: make(http.Header), status: http.StatusOK}
}

func (r *recWriter) Header() http.Header                { return r.hdr }
func (r *recWriter) WriteHeader(code int)               { r.status = code }
func (r *recWriter) Flush()                             {} // no-op: satisfies http.Flusher
func (r *recWriter) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, b...)
	return len(b), nil
}
func (r *recWriter) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// failWriter returns an error on every Write call.
type failWriter struct{ hdr http.Header }

func newFailWriter() *failWriter { return &failWriter{hdr: make(http.Header)} }
func (f *failWriter) Header() http.Header { return f.hdr }
func (f *failWriter) WriteHeader(int)     {}
func (f *failWriter) Flush()              {}
func (f *failWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("simulated write error")
}

// blockWriter blocks Write for the given duration (simulates slow/stale connection).
type blockWriter struct {
	mu      sync.Mutex
	hdr     http.Header
	blockMS int
	written atomic.Int32
}

func newBlockWriter(blockMS int) *blockWriter {
	return &blockWriter{hdr: make(http.Header), blockMS: blockMS}
}
func (b *blockWriter) Header() http.Header { return b.hdr }
func (b *blockWriter) WriteHeader(int)     {}
func (b *blockWriter) Flush()              {}
func (b *blockWriter) Write(data []byte) (int, error) {
	time.Sleep(time.Duration(b.blockMS) * time.Millisecond)
	b.written.Add(int32(len(data)))
	return len(data), nil
}

// plainWriter implements ResponseWriter but NOT http.Flusher.
// Used to test the "streaming not supported" error path.
type plainWriter struct{ hdr http.Header }

func (p *plainWriter) Header() http.Header            { return p.hdr }
func (p *plainWriter) Write(b []byte) (int, error)    { return len(b), nil }
func (p *plainWriter) WriteHeader(int)                {}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewBroadcaster_InitialState(t *testing.T) {
	b := NewBroadcaster()
	if b == nil {
		t.Fatal("NewBroadcaster returned nil")
	}
	if b.clients == nil {
		t.Fatal("clients map must be initialised")
	}
	if n := b.ClientCount(); n != 0 {
		t.Fatalf("expected 0 clients, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// AddClient
// ---------------------------------------------------------------------------

func TestAddClient_RequiresFlusher(t *testing.T) {
	b := NewBroadcaster()
	p := &plainWriter{hdr: make(http.Header)}
	_, err := b.AddClient(p)
	if err == nil {
		t.Fatal("expected error for non-Flusher writer")
	}
	if b.ClientCount() != 0 {
		t.Fatalf("no client should be registered on error, got %d", b.ClientCount())
	}
}

func TestAddClient_AssignsUniqueSequentialIDs(t *testing.T) {
	b := NewBroadcaster()
	seen := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		c, err := b.AddClient(newRecWriter())
		if err != nil {
			t.Fatalf("AddClient failed: %v", err)
		}
		if c.ID == "" {
			t.Fatal("empty client ID")
		}
		if _, dup := seen[c.ID]; dup {
			t.Fatalf("duplicate ID: %s", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	if b.ClientCount() != 20 {
		t.Fatalf("expected 20 clients, got %d", b.ClientCount())
	}
}

func TestAddClient_ClientStructFields(t *testing.T) {
	b := NewBroadcaster()
	w := newRecWriter()
	c, err := b.AddClient(w)
	if err != nil {
		t.Fatal(err)
	}
	if c.Writer == nil {
		t.Error("Writer must be set")
	}
	if c.Flusher == nil {
		t.Error("Flusher must be set")
	}
	if c.Done == nil {
		t.Error("Done channel must be non-nil")
	}
	// Done channel must not be closed yet
	select {
	case <-c.Done:
		t.Error("Done channel must be open after AddClient")
	default:
	}
}

// ---------------------------------------------------------------------------
// RemoveClient
// ---------------------------------------------------------------------------

func TestRemoveClient_DecrementsCount(t *testing.T) {
	b := NewBroadcaster()
	c, _ := b.AddClient(newRecWriter())
	if b.ClientCount() != 1 {
		t.Fatal("expected 1 client after add")
	}
	b.RemoveClient(c)
	if b.ClientCount() != 0 {
		t.Fatalf("expected 0 after remove, got %d", b.ClientCount())
	}
}

func TestRemoveClient_ClosesDoneChannel(t *testing.T) {
	b := NewBroadcaster()
	c, _ := b.AddClient(newRecWriter())
	b.RemoveClient(c)
	select {
	case <-c.Done:
		// correct: channel closed
	default:
		t.Error("Done channel must be closed by RemoveClient")
	}
}

func TestRemoveClient_UnregisteredClientClosesItsDone(t *testing.T) {
	b := NewBroadcaster()
	// Client that was never registered via AddClient
	phantom := &Client{ID: "phantom", Done: make(chan struct{})}
	b.RemoveClient(phantom) // must not panic
	select {
	case <-phantom.Done:
		// correct
	default:
		t.Error("Done channel must be closed even for unregistered clients")
	}
	if b.ClientCount() != 0 {
		t.Fatalf("count must stay 0, got %d", b.ClientCount())
	}
}

// ---------------------------------------------------------------------------
// removeClientByID
// ---------------------------------------------------------------------------

func TestRemoveClientByID_RemovesRegistered(t *testing.T) {
	b := NewBroadcaster()
	c, _ := b.AddClient(newRecWriter())
	b.removeClientByID(c.ID)
	if b.ClientCount() != 0 {
		t.Fatalf("expected 0, got %d", b.ClientCount())
	}
	select {
	case <-c.Done:
	default:
		t.Error("Done channel must be closed")
	}
}

func TestRemoveClientByID_AlreadyClosedDone_NoPanic(t *testing.T) {
	b := NewBroadcaster()
	c, _ := b.AddClient(newRecWriter())
	close(c.Done) // pre-close
	b.removeClientByID(c.ID) // must not panic (double-close guard)
	if b.ClientCount() != 0 {
		t.Fatalf("expected 0, got %d", b.ClientCount())
	}
}

func TestRemoveClientByID_NonExistentID_NoPanic(t *testing.T) {
	b := NewBroadcaster()
	b.removeClientByID("no-such-id") // must not panic
}

// ---------------------------------------------------------------------------
// Broadcast
// ---------------------------------------------------------------------------

func TestBroadcast_EmitsDataFrameFormat(t *testing.T) {
	b := NewBroadcaster()
	w := newRecWriter()
	_, err := b.AddClient(w)
	if err != nil {
		t.Fatal(err)
	}

	payload := map[string]string{"event": "ping"}
	b.Broadcast(payload)

	// Broadcast writes are concurrent; wait briefly
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.HasPrefix(w.body(), "data: ") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	body := w.body()
	if !strings.HasPrefix(body, "data: ") {
		t.Fatalf("expected SSE data: prefix, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("expected double newline terminator, got %q", body)
	}

	// Payload must be valid JSON embedded in the frame
	raw := strings.TrimPrefix(body, "data: ")
	raw = strings.TrimSuffix(raw, "\n\n")
	var got map[string]string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("frame payload not valid JSON: %v", err)
	}
	if got["event"] != "ping" {
		t.Errorf("unexpected payload: %v", got)
	}
}

func TestBroadcast_NoClientsDoesNotPanic(t *testing.T) {
	b := NewBroadcaster()
	b.Broadcast(map[string]string{"k": "v"}) // must not panic or block
}

func TestBroadcast_DeliverToAllClients(t *testing.T) {
	b := NewBroadcaster()
	const n = 5
	writers := make([]*recWriter, n)
	for i := range writers {
		writers[i] = newRecWriter()
		b.AddClient(writers[i]) //nolint:errcheck
	}

	b.Broadcast(map[string]int{"seq": 1})

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		ready := 0
		for _, w := range writers {
			if strings.Contains(w.body(), "data:") {
				ready++
			}
		}
		if ready == n {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i, w := range writers {
		if !strings.Contains(w.body(), "data:") {
			t.Errorf("client %d did not receive broadcast", i)
		}
	}
}

func TestBroadcast_SkipsClosedDoneClients(t *testing.T) {
	b := NewBroadcaster()
	w := newRecWriter()
	c, _ := b.AddClient(w)

	// Signal the client is gone before broadcast
	close(c.Done)

	b.Broadcast(map[string]string{"type": "ignored"})
	time.Sleep(50 * time.Millisecond)

	// The writer must have received nothing (client was skipped)
	if body := w.body(); body != "" {
		t.Errorf("disconnected client should receive nothing, got %q", body)
	}
}

func TestBroadcast_WriterError_ClientMarkedDead(t *testing.T) {
	b := NewBroadcaster()
	fw := newFailWriter()
	c, err := b.AddClient(fw)
	if err != nil {
		t.Fatal(err)
	}

	b.Broadcast(map[string]string{"k": "v"})

	// After broadcast completes the dead client must be removed
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.ClientCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.ClientCount() != 0 {
		t.Errorf("expected dead client to be removed, count=%d", b.ClientCount())
	}
	// Done channel should have been closed by removeClientByID
	select {
	case <-c.Done:
	default:
		t.Error("dead client's Done channel should be closed")
	}
}

func TestBroadcast_UnmarshalableData_NoSend(t *testing.T) {
	b := NewBroadcaster()
	w := newRecWriter()
	b.AddClient(w) //nolint:errcheck

	// Channels cannot be JSON-marshalled; Broadcast should log and return, no data written
	ch := make(chan int)
	b.Broadcast(ch)
	time.Sleep(30 * time.Millisecond)

	if body := w.body(); body != "" {
		t.Errorf("unmarshalable data must not produce output, got %q", body)
	}
}

// ---------------------------------------------------------------------------
// writeToClient
// ---------------------------------------------------------------------------

func TestWriteToClient_SuccessDelivery(t *testing.T) {
	b := NewBroadcaster()
	w := newRecWriter()
	c, _ := b.AddClient(w)

	deadCh := make(chan string, 1)
	b.writeToClient(c, "data: hello\n\n", deadCh, nil)

	// No dead client should be reported
	select {
	case id := <-deadCh:
		t.Errorf("healthy client marked dead: %s", id)
	default:
	}
	if !strings.Contains(w.body(), "hello") {
		t.Errorf("write not delivered, body=%q", w.body())
	}
}

func TestWriteToClient_WriteError_ReportsDead(t *testing.T) {
	b := NewBroadcaster()
	fw := newFailWriter()
	c, _ := b.AddClient(fw)

	deadCh := make(chan string, 1)
	b.writeToClient(c, "data: x\n\n", deadCh, nil)

	select {
	case id := <-deadCh:
		if id != c.ID {
			t.Errorf("expected %s, got %s", c.ID, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("expected dead-client report, got none")
	}
}

func TestWriteToClient_Timeout_ReportsDead(t *testing.T) {
	// The block writer will block longer than WriteTimeout (2s).
	// We use a duration > WriteTimeout to ensure the timeout path fires.
	b := NewBroadcaster()
	bw := newBlockWriter(int((WriteTimeout + 500*time.Millisecond).Milliseconds()))
	c, _ := b.AddClient(bw)

	deadCh := make(chan string, 1)

	start := time.Now()
	b.writeToClient(c, "data: slow\n\n", deadCh, nil)
	elapsed := time.Since(start)

	// writeToClient should return after ~WriteTimeout (not blockFor)
	if elapsed > WriteTimeout+time.Second {
		t.Errorf("writeToClient took too long: %v (expected ~%v)", elapsed, WriteTimeout)
	}

	select {
	case id := <-deadCh:
		if id != c.ID {
			t.Errorf("expected %s, got %s", c.ID, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("expected dead-client report for timed-out write")
	}
}

func TestWriteToClient_DoneClosedDuringWrite_NoPanic(t *testing.T) {
	b := NewBroadcaster()
	bw := newBlockWriter(int((WriteTimeout + 500*time.Millisecond).Milliseconds()))
	c, _ := b.AddClient(bw)

	// Close Done while write is in progress
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.RemoveClient(c)
	}()

	deadCh := make(chan string, 2)
	var wg sync.WaitGroup
	// must not panic; pass wg so the test can join the inner goroutine
	b.writeToClient(c, "data: race\n\n", deadCh, &wg)
	// Join the inner goroutine before the test returns so the blockWriter's
	// fake ResponseWriter is not used after the test completes. Production
	// Broadcast does not join it — deadClientsCh is buffered and never closed,
	// so a late send is safe.
	wg.Wait()
}

// TestBroadcast_NoPanicOnSlowClientDoneClose verifies that Broadcast does not
// panic with "send on closed channel" when a client's Done channel is closed
// while the background write goroutine is still holding WriteMu. This is the
// real scenario the CRIT finding described: writeToClient returns (via the
// client.Done case) before the inner goroutine finishes, and the still-running
// inner goroutine later sends its dead-client report. The fix — a buffered,
// never-closed deadClientsCh sized for every possible send — is exercised on
// the real Broadcast code path here.
func TestBroadcast_NoPanicOnSlowClientDoneClose(t *testing.T) {
	b := NewBroadcaster()
	// blockWriter with a duration longer than WriteTimeout so the inner goroutine
	// outlives writeToClient's select — the real race condition scenario.
	bw := newBlockWriter(int((WriteTimeout + 200*time.Millisecond).Milliseconds()))
	c, _ := b.AddClient(bw)

	// Remove the client very shortly after Broadcast starts — this triggers the
	// client.Done case inside writeToClient while the inner goroutine is blocked.
	go func() {
		time.Sleep(5 * time.Millisecond)
		b.RemoveClient(c)
	}()

	// Broadcast must complete without panicking. Run under race detector (go test
	// -race) to catch any residual data races.
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Broadcast(map[string]string{"type": "test"})
	}()

	select {
	case <-done:
		// success — no panic
	case <-time.After(WriteTimeout + 2*time.Second):
		t.Fatal("Broadcast did not return within expected time")
	}
}

// TestBroadcast_ReturnsWithinTimeoutOnWedgedClient verifies the codex P1
// finding: a client whose Write blocks far past WriteTimeout must NOT stall
// Broadcast — Broadcast returns after ~WriteTimeout because it does not join
// the inner write goroutines.
func TestBroadcast_ReturnsWithinTimeoutOnWedgedClient(t *testing.T) {
	b := NewBroadcaster()
	// Wedged client: blocks 3x WriteTimeout.
	bw := newBlockWriter(int((3 * WriteTimeout).Milliseconds()))
	wedged, _ := b.AddClient(bw)

	start := time.Now()
	b.Broadcast(map[string]string{"type": "test"})
	elapsed := time.Since(start)

	// Broadcast must return after ~WriteTimeout, not after the wedged Write.
	if elapsed > WriteTimeout+time.Second {
		t.Fatalf("Broadcast blocked on wedged client: %v (expected ~%v)", elapsed, WriteTimeout)
	}

	// The wedged client must have been reported dead and removed.
	b.mu.RLock()
	_, stillThere := b.clients[wedged.ID]
	b.mu.RUnlock()
	if stillThere {
		t.Error("wedged client should have been removed after timeout")
	}
}

// ---------------------------------------------------------------------------
// ClientCount
// ---------------------------------------------------------------------------

func TestClientCount_ReflectsAddRemove(t *testing.T) {
	b := NewBroadcaster()

	if b.ClientCount() != 0 {
		t.Fatal("initial count must be 0")
	}

	var clients []*Client
	for i := 0; i < 8; i++ {
		c, _ := b.AddClient(newRecWriter())
		clients = append(clients, c)
	}
	if n := b.ClientCount(); n != 8 {
		t.Fatalf("expected 8, got %d", n)
	}

	for _, c := range clients[:4] {
		b.RemoveClient(c)
	}
	if n := b.ClientCount(); n != 4 {
		t.Fatalf("expected 4, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety
// ---------------------------------------------------------------------------

func TestConcurrency_ConcurrentAddBroadcastRemove(t *testing.T) {
	b := NewBroadcaster()
	var wg sync.WaitGroup

	// Concurrent adds
	const adders = 20
	clients := make(chan *Client, adders)
	for i := 0; i < adders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := b.AddClient(newRecWriter())
			if err == nil {
				clients <- c
			}
		}()
	}

	// Concurrent broadcasts
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b.Broadcast(map[string]int{"i": i})
		}(i)
	}

	wg.Wait()
	close(clients)

	// Concurrent removes
	var wg2 sync.WaitGroup
	for c := range clients {
		c := c
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			b.RemoveClient(c)
		}()
	}
	wg2.Wait()

	// Race detector validates no data races; count may vary
	if n := b.ClientCount(); n < 0 {
		t.Fatalf("negative count: %d", n)
	}
}

func TestConcurrency_ConcurrentRemoveClientByID(t *testing.T) {
	b := NewBroadcaster()
	c, _ := b.AddClient(newRecWriter())

	// Two goroutines racing to remove the same client by ID
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.removeClientByID(c.ID)
		}()
	}
	wg.Wait() // must not panic (double-close guard in removeClientByID)
}

// ---------------------------------------------------------------------------
// WriteTimeout constant
// ---------------------------------------------------------------------------

func TestWriteTimeoutValue(t *testing.T) {
	if WriteTimeout != 2*time.Second {
		t.Errorf("expected 2s WriteTimeout, got %v", WriteTimeout)
	}
}

// ---------------------------------------------------------------------------
// KeepaliveInterval constant
// ---------------------------------------------------------------------------

func TestKeepaliveIntervalValue(t *testing.T) {
	if KeepaliveInterval != 25*time.Second {
		t.Errorf("expected 25s KeepaliveInterval, got %v", KeepaliveInterval)
	}
}

// ---------------------------------------------------------------------------
// HandleSSE
// ---------------------------------------------------------------------------

func TestHandleSSE_RequiresFlusher_Returns500(t *testing.T) {
	b := NewBroadcaster()
	p := &plainWriter{hdr: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	b.HandleSSE(p, req)
	// Non-flusher: AddClient returns error → http.Error with 500 is written
	// plainWriter ignores status, so we just verify no client was added
	if b.ClientCount() != 0 {
		t.Errorf("no client should be registered for non-Flusher, got %d", b.ClientCount())
	}
}

func TestHandleSSE_SetsSSEHeaders(t *testing.T) {
	b := NewBroadcaster()
	srv := httptest.NewServer(http.HandlerFunc(b.HandleSSE))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	checks := map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, want := range checks {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("header %s: got %q, want %q", k, got, want)
		}
	}
}

func TestHandleSSE_SendsConnectedMessage(t *testing.T) {
	b := NewBroadcaster()
	srv := httptest.NewServer(http.HandlerFunc(b.HandleSSE))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	// Read just enough bytes to capture the initial connected frame
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	if !strings.Contains(body, `"type":"connected"`) {
		t.Errorf("expected connected message, got %q", body)
	}
	if !strings.Contains(body, `"clientId"`) {
		t.Errorf("expected clientId in connected message, got %q", body)
	}
}

func TestHandleSSE_ClientRemovedOnContextDone(t *testing.T) {
	b := NewBroadcaster()
	srv := httptest.NewServer(http.HandlerFunc(b.HandleSSE))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}

	// Wait for client to be registered
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.ClientCount() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if b.ClientCount() != 1 {
		resp.Body.Close()
		t.Fatalf("expected 1 client, got %d", b.ClientCount())
	}

	// Close the response body to signal disconnect to the server
	resp.Body.Close()
	srv.Close()

	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.ClientCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b.ClientCount() != 0 {
		t.Errorf("client must be removed after disconnect, got %d", b.ClientCount())
	}
}
