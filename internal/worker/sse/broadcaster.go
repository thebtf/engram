// Package sse provides Server-Sent Events broadcasting for engram.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// WriteTimeout is the timeout for writing to SSE clients.
	// Prevents blocking on stale connections.
	WriteTimeout = 2 * time.Second
)

// Client represents a connected SSE client.
type Client struct {
	Writer  http.ResponseWriter
	Flusher http.Flusher
	Done    chan struct{}
	ID      string
	// WriteMu serializes writes to Writer/Flusher. Both the broadcast goroutine
	// and the keepalive ticker write to the same ResponseWriter concurrently;
	// without this lock, interleaved bytes would corrupt the SSE stream.
	WriteMu sync.Mutex
}

// Broadcaster manages SSE client connections and message broadcasting.
type Broadcaster struct {
	clients map[string]*Client
	mu      sync.RWMutex
	seq     int
}

// NewBroadcaster returns an empty SSE broadcaster ready to accept connections.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		clients: make(map[string]*Client),
	}
}

// AddClient registers a new SSE connection and returns the Client.
// Returns an error when the ResponseWriter does not support flushing,
// which is required for SSE streaming.
func (b *Broadcaster) AddClient(w http.ResponseWriter) (*Client, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	b.mu.Lock()
	b.seq++
	id := fmt.Sprintf("client-%d", b.seq)
	c := &Client{
		ID:      id,
		Writer:  w,
		Flusher: flusher,
		Done:    make(chan struct{}),
	}
	b.clients[id] = c
	n := len(b.clients)
	b.mu.Unlock()

	log.Debug().
		Str("clientId", id).
		Int("totalClients", n).
		Msg("SSE client connected")

	return c, nil
}

// RemoveClient unregisters the client and signals its Done channel.
func (b *Broadcaster) RemoveClient(client *Client) {
	b.mu.Lock()
	if b.clients[client.ID] == client {
		delete(b.clients, client.ID)
	}
	if client.Done != nil {
		select {
		case <-client.Done:
		default:
			close(client.Done)
		}
	}
	n := len(b.clients)
	b.mu.Unlock()

	log.Debug().
		Str("clientId", client.ID).
		Int("totalClients", n).
		Msg("SSE client disconnected")
}

// removeClientByID removes a client by ID (for dead client cleanup).
func (b *Broadcaster) removeClientByID(id string) {
	b.mu.Lock()
	c, found := b.clients[id]
	if found {
		delete(b.clients, id)
		if c.Done != nil {
			select {
			case <-c.Done:
			default:
				close(c.Done)
			}
		}
	}
	n := len(b.clients)
	b.mu.Unlock()

	log.Debug().
		Str("clientId", id).
		Int("totalClients", n).
		Msg("Dead SSE client removed")
}

// Broadcast sends a message to all connected clients.
// Uses non-blocking writes with timeout to prevent stale connections from blocking.
func (b *Broadcaster) Broadcast(data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal SSE data")
		return
	}

	message := fmt.Sprintf("data: %s\n\n", jsonData)

	b.mu.RLock()
	clients := make([]*Client, 0, len(b.clients))
	for _, client := range b.clients {
		clients = append(clients, client)
	}
	b.mu.RUnlock()

	if len(clients) == 0 {
		return
	}

	// Use a channel to collect dead clients from concurrent writes.
	// Capacity is 2 slots per client (timeout path + write-error path can both
	// fire for the same client), so no send ever blocks. The channel is never
	// closed: an inner write goroutine stuck past WriteTimeout may send after
	// Broadcast returns, and a send on a never-closed buffered channel is safe.
	deadClientsCh := make(chan string, 2*len(clients))
	var wg sync.WaitGroup // tracks writeToClient calls

	for _, client := range clients {
		select {
		case <-client.Done:
			continue
		default:
			wg.Add(1)
			go func(c *Client) {
				defer wg.Done()
				b.writeToClient(c, message, deadClientsCh, nil)
			}(client)
		}
	}

	// Wait only for writeToClient calls — each returns within WriteTimeout even
	// when a client's Write is wedged, so one stuck client cannot stall every
	// broadcast caller. Inner write goroutines that outlive the timeout are NOT
	// awaited; their late dead-client reports land in the buffered channel and
	// the client is re-detected on the next Broadcast pass.
	wg.Wait()

	// Drain the dead-client reports available now, without closing the channel.
	for {
		select {
		case clientID := <-deadClientsCh:
			b.removeClientByID(clientID)
		default:
			return
		}
	}
}

// writeToClient writes a message to a single client with timeout.
// deadCh must be buffered with enough capacity for every possible send (two
// per client: timeout path and write-error path) and must never be closed —
// the inner write goroutine may outlive this call and send late. wg, when
// non-nil, tracks the inner goroutine's lifetime (used by tests to join it).
func (b *Broadcaster) writeToClient(client *Client, message string, deadCh chan<- string, wg *sync.WaitGroup) {
	// Use a done channel to detect when the background write finishes.
	done := make(chan struct{})

	if wg != nil {
		wg.Add(1)
	}
	go func() {
		defer close(done)
		if wg != nil {
			defer wg.Done()
		}
		client.WriteMu.Lock()
		defer client.WriteMu.Unlock()
		select {
		case <-client.Done:
			return
		default:
		}
		_, err := client.Writer.Write([]byte(message))
		if err != nil {
			log.Debug().
				Str("clientId", client.ID).
				Err(err).
				Msg("Failed to write to SSE client, marking for removal")
			deadCh <- client.ID
			return
		}
		client.Flusher.Flush()
	}()

	select {
	case <-done:
		// Write completed successfully
	case <-time.After(WriteTimeout):
		log.Warn().
			Str("clientId", client.ID).
			Dur("timeout", WriteTimeout).
			Msg("SSE write timed out, marking client for removal")
		deadCh <- client.ID
	case <-client.Done:
		// Client disconnected during write
	}
}

// ClientCount returns the number of connected clients.
func (b *Broadcaster) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// KeepaliveInterval is how often the SSE handler sends a comment-line keepalive.
// These comments help prevent idle intermediaries from closing the connection;
// they do not extend an absolute HTTP server write deadline.
const KeepaliveInterval = 25 * time.Second

// HandleSSE streams live dashboard events; frames contain JSON in SSE data fields.
// @Summary Stream dashboard events
// @Description Sends a connected object and live JSON event objects; a concurrent broadcast can arrive before the connected object. SSE comment keepalives occur every 25 seconds. No event IDs or replay are provided; reconnecting starts a new stream.
// @Description Available before database readiness, subject to authentication and rate limiting. A query token is supported for browser SSE clients.
// @Description A failed or timed-out broadcast removes the subscriber and ends its stream once any in-flight write finishes. A permanently blocked writer can delay stream termination. Reconnect and refresh state; missed events cannot be replayed.
// @Description Success is text/event-stream; pre-stream http.Error responses are text/plain. Swagger 2 lists media types at operation scope, not per status.
// @Tags System
// @Produce text/event-stream,plain
// @Security ApiKeyAuth
// @Security StreamQueryToken
// @Success 200 {string} string "SSE stream of JSON data frames and comment keepalives"
// @Failure 401 {string} string "unauthorized"
// @Failure 403 {string} string "keycard class forbidden on this route"
// @Failure 429 {string} string "rate limit exceeded"
// @Failure 500 {string} string "streaming not supported or authentication store unavailable"
// @Router /api/events [get]
func (b *Broadcaster) HandleSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Disable proxy buffering — nginx and similar reverse proxies will buffer
	// SSE responses by default, causing the stream to appear stuck.
	w.Header().Set("X-Accel-Buffering", "no")

	client, err := b.AddClient(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() {
		b.RemoveClient(client)
		// Join any in-flight write before the handler releases the ResponseWriter.
		// Writers queued on WriteMu check Done before touching it.
		client.WriteMu.Lock()
		client.WriteMu.Unlock()
	}()

	// Send initial connection message unless a broadcast already removed us.
	client.WriteMu.Lock()
	select {
	case <-client.Done:
		client.WriteMu.Unlock()
		return
	default:
	}
	_, err = fmt.Fprintf(w, "data: {\"type\":\"connected\",\"clientId\":\"%s\"}\n\n", client.ID)
	if err == nil {
		client.Flusher.Flush()
	}
	client.WriteMu.Unlock()
	if err != nil {
		return
	}

	// SSE comment lines are ignored by clients but keep the connection active.
	// They cannot reset an absolute HTTP server write deadline.
	ticker := time.NewTicker(KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-client.Done:
			return
		case <-ticker.C:
			client.WriteMu.Lock()
			select {
			case <-client.Done:
				client.WriteMu.Unlock()
				return
			default:
			}
			if _, werr := fmt.Fprint(w, ": keepalive\n\n"); werr != nil {
				client.WriteMu.Unlock()
				return
			}
			client.Flusher.Flush()
			client.WriteMu.Unlock()
		}
	}
}
