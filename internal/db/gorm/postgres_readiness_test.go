package gorm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTransientPostgresConnectionError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"refused", syscall.ECONNREFUSED, true},
		{"reset", syscall.ECONNRESET, true},
		{"closed connection", io.EOF, true},
		{"attempt timeout", context.DeadlineExceeded, true},
		{"wrapped temporary DNS SERVFAIL", fmt.Errorf("connect failed: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}}), true},
		{"wrapped DNS not found with transient flags", fmt.Errorf("connect failed: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", IsNotFound: true, IsTemporary: true, IsTimeout: true}}), false},
		{"DNS timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, true},
		{"database starting", &pgconn.PgError{Code: "57P03"}, true},
		{"invalid password", &pgconn.PgError{Code: "28P01"}, false},
		{"unknown database", &pgconn.PgError{Code: "3D000"}, false},
		{"invalid config", errors.New("invalid configuration"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientPostgresConnectionError(tc.err); got != tc.retry {
				t.Fatalf("retry = %v, want %v", got, tc.retry)
			}
		})
	}
}

func TestNewStoreWhenAvailableCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	store, err := NewStoreWhenAvailable(ctx, Config{DSN: "postgres://postgres@127.0.0.1:1/pg_recovery_test?sslmode=disable"})
	if store != nil {
		_ = store.Close()
		t.Fatal("cancelled startup returned a store")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancelled startup did not stop promptly")
	}
}

func TestNewStoreWhenAvailableStopsDuringOutage(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	attempted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case attempted <- struct{}{}:
			default:
			}
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		store, err := NewStoreWhenAvailable(ctx, Config{DSN: "postgres://postgres@" + listener.Addr().String() + "/pg_recovery_test?sslmode=disable"})
		if store != nil {
			_ = store.Close()
		}
		done <- err
	}()
	select {
	case <-attempted:
	case <-time.After(3 * time.Second):
		t.Fatal("no connection attempt")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled outage error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("database retry did not stop")
	}
}

func TestNewStoreWhenAvailableRejectsInvalidRole(t *testing.T) {
	dsn := os.Getenv("ENGRAM_PG_RECOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("requires a dedicated local PostgreSQL fixture")
	}
	target, err := url.Parse(dsn)
	if err != nil || target.Scheme != "postgres" || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Path != "/pg_recovery_test" {
		t.Fatal("invalid fixture endpoint")
	}
	target.User = url.User("engram_nonexistent_recovery_role")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := NewStoreWhenAvailable(ctx, Config{DSN: target.String()})
	if store != nil {
		_ = store.Close()
		t.Fatal("invalid role returned a store")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "28000" {
		t.Fatalf("invalid role did not fail with PostgreSQL authorization error: %v", err)
	}
}
