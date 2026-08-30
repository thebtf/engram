package legacyrelay

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/thebtf/mcp-mux/muxcore/ipc"
)

const defaultMaxConcurrentConnections = 32

// ListenerConfig keeps endpoint ownership separate from relay semantics. A
// disabled listener performs no filesystem or socket effect.
type ListenerConfig struct {
	Enabled       bool
	BaseDir       string
	Generation    DaemonGeneration
	Relay         *Relay
	MaxConcurrent int
}

// Listener owns one dark/private IPC endpoint, its locator, and bounded accept
// concurrency. It is safe to close more than once.
type Listener struct {
	disabled    bool
	endpoint    string
	locatorPath string
	generation  DaemonGeneration
	relay       *Relay
	listener    net.Listener
	cancel      context.CancelFunc
	acceptDone  chan struct{}
	done        chan struct{}
	semaphore   chan struct{}
	closeOnce   sync.Once
	closeErr    error
	workers     sync.WaitGroup
}

// StartListener opens and publishes the relay endpoint only when explicitly
// enabled. Locator publication happens after listen succeeds; any later setup
// failure closes and cleans the endpoint before returning.
func StartListener(parent context.Context, config ListenerConfig) (*Listener, error) {
	if !config.Enabled {
		return &Listener{disabled: true, acceptDone: closedChannel(), done: closedChannel()}, nil
	}
	if parent == nil || config.Relay == nil || !config.Generation.valid() {
		return nil, errors.New("legacy relay listener configuration is incomplete")
	}
	maxConcurrent := config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentConnections
	}
	endpoint, err := RuntimeEndpoint(config.BaseDir, config.Generation)
	if err != nil {
		return nil, err
	}
	locatorPath := RuntimeLocatorPath(config.BaseDir)
	listener, err := ipc.Listen(endpoint)
	if err != nil {
		return nil, fmt.Errorf("listen on legacy relay endpoint: %w", err)
	}
	locator := Locator{Protocol: Protocol, Generation: config.Generation, Endpoint: endpoint}
	if err := PublishLocator(locatorPath, locator); err != nil {
		_ = listener.Close()
		ipc.Cleanup(endpoint)
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	result := &Listener{
		endpoint:    endpoint,
		locatorPath: locatorPath,
		generation:  config.Generation,
		relay:       config.Relay,
		listener:    listener,
		cancel:      cancel,
		acceptDone:  make(chan struct{}),
		done:        make(chan struct{}),
		semaphore:   make(chan struct{}, maxConcurrent),
	}
	go result.serve(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-result.acceptDone:
		}
		_ = result.Close()
	}()
	return result, nil
}

func (l *Listener) Endpoint() string {
	if l == nil || l.disabled {
		return ""
	}
	return l.endpoint
}

func (l *Listener) LocatorPath() string {
	if l == nil || l.disabled {
		return ""
	}
	return l.locatorPath
}

func (l *Listener) Done() <-chan struct{} {
	if l == nil {
		return closedChannel()
	}
	return l.done
}

func (l *Listener) serve(ctx context.Context) {
	defer close(l.acceptDone)
	for {
		connection, err := l.listener.Accept()
		if err != nil {
			return
		}
		log.Print("legacy relay connection accepted")
		select {
		case l.semaphore <- struct{}{}:
			log.Print("legacy relay connection queued")
			l.workers.Add(1)
			go func(conn net.Conn) {
				log.Print("legacy relay connection serving")
				defer l.workers.Done()
				defer func() { <-l.semaphore }()
				l.relay.ServeConn(ctx, conn)
			}(connection)
		default:
			log.Print("legacy relay connection saturated")
			_ = connection.Close()
		}
	}
}

// Close stops accepting, waits for the accept loop, removes only the locator
// still owned by this exact generation/endpoint, and cleans the logical IPC
// path. It never removes a successor generation's locator.
func (l *Listener) Close() error {
	if l == nil || l.disabled {
		return nil
	}
	l.closeOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		if l.listener != nil {
			l.closeErr = l.listener.Close()
		}
		<-l.acceptDone
		l.workers.Wait()
		if err := removeLocatorIfOwned(l.locatorPath, l.generation, l.endpoint); err != nil {
			l.closeErr = errors.Join(l.closeErr, err)
		}
		ipc.Cleanup(l.endpoint)
		close(l.done)
	})
	return l.closeErr
}

func removeLocatorIfOwned(path string, generation DaemonGeneration, endpoint string) error {
	locator, err := ReadLocator(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read owned legacy relay locator: %w", err)
	}
	if locator.Protocol != Protocol || locator.Generation != generation || locator.Endpoint != endpoint {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy relay locator: %w", err)
	}
	return nil
}

func closedChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
