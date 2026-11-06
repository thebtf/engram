package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type startupPostgresRelay struct {
	listener    net.Listener
	upstream    string
	available   atomic.Bool
	attempts    atomic.Int64
	mu          sync.Mutex
	connections map[net.Conn]struct{}
}

func newStartupPostgresRelay(t *testing.T, upstream string) *startupPostgresRelay {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &startupPostgresRelay{listener: listener, upstream: upstream, connections: make(map[net.Conn]struct{})}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			r.attempts.Add(1)
			if !r.available.Load() {
				_ = client.Close()
				continue
			}
			server, err := net.DialTimeout("tcp", r.upstream, time.Second)
			if err != nil {
				_ = client.Close()
				continue
			}
			r.mu.Lock()
			if !r.available.Load() {
				r.mu.Unlock()
				_ = client.Close()
				_ = server.Close()
				continue
			}
			r.connections[client] = struct{}{}
			r.connections[server] = struct{}{}
			r.mu.Unlock()
			go func() {
				defer func() {
					_ = client.Close()
					_ = server.Close()
					r.mu.Lock()
					delete(r.connections, client)
					delete(r.connections, server)
					r.mu.Unlock()
				}()
				go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
				_, _ = io.Copy(client, server)
			}()
		}
	}()
	t.Cleanup(func() { r.disconnect(); _ = listener.Close() })
	return r
}

func (r *startupPostgresRelay) disconnect() {
	r.mu.Lock()
	r.available.Store(false)
	defer r.mu.Unlock()
	for conn := range r.connections {
		_ = conn.Close()
	}
}

func recoveryWait(t *testing.T, duration time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(message)
}

// The DSN must identify a disposable loopback database dedicated to this test.
func TestPostgresStartupRecoverySameService(t *testing.T) {
	dsn := os.Getenv("ENGRAM_PG_RECOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("requires a dedicated local PostgreSQL fixture")
	}
	target, err := url.Parse(dsn)
	if err != nil || target.Scheme != "postgres" || target.Hostname() != "127.0.0.1" || target.Port() == "" || target.Path != "/pg_recovery_test" {
		t.Fatal("fixture DSN must identify the dedicated loopback pg_recovery_test database")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ENGRAM_") {
			t.Setenv(name, "")
		}
	}
	relay := newStartupPostgresRelay(t, target.Host)
	target.Host = relay.listener.Addr().String()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ENGRAM_DB_PATH", filepath.Join(home, ".engram", "engram.db"))
	t.Setenv("DATABASE_DSN", target.String())
	t.Setenv("DATABASE_MAX_CONNS", "2")
	t.Setenv("ENGRAM_AUTH_DISABLED", "true")
	t.Setenv("ENGRAM_AUTH_ADMIN_TOKEN", "")
	t.Setenv("ENGRAM_TELEMETRY_ENABLED", "false")
	t.Setenv("ENGRAM_ENCRYPTION_KEY", strings.Repeat("51", 32))
	service, err := NewService("postgres-startup-recovery-test", nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	server := httptest.NewServer(service.router)
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	recoveryWait(t, 10*time.Second, func() bool { return relay.attempts.Load() > 0 }, "backend never attempted the isolated unavailable database")
	response, err := client.Get(server.URL + "/api/issues")
	if err != nil {
		t.Fatalf("unavailable request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", response.StatusCode)
	}
	relay.available.Store(true)
	recoveryWait(t, 30*time.Second, func() bool {
		response, err := client.Get(server.URL + "/api/ready")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, "same service did not recover after isolated PostgreSQL became available")
	response, err = client.Get(server.URL + "/api/issues")
	if err != nil {
		t.Fatalf("recovered issue read: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("recovered issue status = %d, want 200", response.StatusCode)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["issues"] == nil {
		t.Fatalf("recovered issue reader returned invalid envelope: %v", err)
	}
	service.initMu.RLock()
	initialStore, initialReaper := service.store, service.projectReaper
	service.initMu.RUnlock()
	if initialStore == nil || initialReaper == nil {
		t.Fatal("startup did not install its database and reaper")
	}
	var initialMigrations int64
	if err := initialStore.DB.Table("migrations").Count(&initialMigrations).Error; err != nil {
		t.Fatalf("count fixture migrations: %v", err)
	}
	const marker = "isolated-postgres-recovery-issue"
	issue := `{"title":"` + marker + `","source_project":"b42c9854-bf02-4d1b-867d-a2a096f9e4d0","target_project":"b42c9854-bf02-4d1b-867d-a2a096f9e4d0","type":"bug"}`
	created, err := client.Post(server.URL+"/api/issues", "application/json", bytes.NewBufferString(issue))
	if err != nil {
		t.Fatalf("create fixture issue once: %v", err)
	}
	var result struct {
		ID int64 `json:"id"`
	}
	err = json.NewDecoder(created.Body).Decode(&result)
	_ = created.Body.Close()
	if err != nil || created.StatusCode != http.StatusCreated || result.ID == 0 {
		t.Fatalf("create fixture issue: status=%d err=%v", created.StatusCode, err)
	}
	var initialIssues int64
	if err := initialStore.DB.Table("issues").Where("title = ?", marker).Count(&initialIssues).Error; err != nil {
		t.Fatalf("count fixture issues: %v", err)
	}
	issueURL := fmt.Sprintf("%s/api/issues/%d", server.URL, result.ID)
	readIssue := func() bool {
		res, err := client.Get(issueURL)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return false
		}
		var body struct {
			Issue struct {
				Title string `json:"title"`
			} `json:"issue"`
		}
		return json.NewDecoder(res.Body).Decode(&body) == nil && body.Issue.Title == marker
	}
	if !readIssue() {
		t.Fatal("same client did not read the fixture issue")
	}
	relay.disconnect()
	if readIssue() {
		t.Fatal("issue read appeared successful while the isolated database was disconnected")
	}
	relay.available.Store(true)
	recoveryWait(t, 30*time.Second, readIssue, "same client did not recover its existing fixture issue after PostgreSQL returned")
	service.initMu.RLock()
	currentStore, currentReaper := service.store, service.projectReaper
	service.initMu.RUnlock()
	if currentStore != initialStore || currentReaper != initialReaper {
		t.Fatal("database loss replaced startup-owned resources")
	}
	var finalMigrations, finalIssues int64
	if err := initialStore.DB.Table("migrations").Count(&finalMigrations).Error; err != nil {
		t.Fatalf("recount fixture migrations: %v", err)
	}
	if err := initialStore.DB.Table("issues").Where("title = ?", marker).Count(&finalIssues).Error; err != nil {
		t.Fatalf("recount fixture issues: %v", err)
	}
	if finalMigrations != initialMigrations || finalIssues != initialIssues {
		t.Fatalf("database recovery duplicated effects: migrations %d -> %d, issues %d -> %d", initialMigrations, finalMigrations, initialIssues, finalIssues)
	}
}
