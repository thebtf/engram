package worker

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type readinessListener struct {
	addr        net.Addr
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func newReadinessListener(addr net.Addr) *readinessListener {
	return &readinessListener{addr: addr, connections: make(chan net.Conn), done: make(chan struct{})}
}

func (l *readinessListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *readinessListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *readinessListener) Addr() net.Addr { return l.addr }

// serveGRPCWhenReady rejects every pre-initialization RPC without creating an
// auth-disabled Engram server. Ready clients reconnect to the real auth-wired server.
func (s *Service) serveGRPCWhenReady(source net.Listener) {
	unavailable := newReadinessListener(source.Addr())
	ready := newReadinessListener(source.Addr())
	var live atomic.Bool
	stub := grpc.NewServer(grpc.UnknownServiceHandler(func(any, grpc.ServerStream) error {
		if s.GetInitError() != nil {
			return status.Error(codes.Unavailable, "service initialization failed")
		}
		return status.Error(codes.Unavailable, "service initializing")
	}))
	go func() { _ = stub.Serve(unavailable) }()
	go func() {
		defer unavailable.Close()
		defer ready.Close()
		for {
			conn, err := source.Accept()
			if err != nil {
				return
			}
			target := unavailable
			if live.Load() {
				target = ready
			}
			select {
			case target.connections <- conn:
			case <-target.done:
				_ = conn.Close()
			case <-s.ctx.Done():
				_ = conn.Close()
				return
			}
		}
	}()
	defer stub.Stop()
	defer func() {
		if s.ctx.Err() != nil {
			_ = source.Close()
		}
	}()
	defer ready.Close()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.initMu.RLock()
		server := s.grpcServer
		s.initMu.RUnlock()
		if server != nil && s.ready.Load() {
			live.Store(true)
			stub.Stop()
			if err := server.Serve(ready); err != nil && s.ctx.Err() == nil {
				log.Error().Err(err).Msg("gRPC server error")
			}
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
