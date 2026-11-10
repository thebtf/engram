package gorm

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	dockerMigrationSlot         = make(chan struct{}, 1)
	dockerMigrationUnreconciled bool
)

func fixtureDockerCommand(ctx context.Context, endpoint string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", endpoint}, args...)...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
		default:
			cmd.Env = append(cmd.Env, entry)
		}
	}
	return cmd
}

// openDockerMigrationDB owns a disposable PostgreSQL container, never DATABASE_DSN.
func openDockerMigrationDB(tb testing.TB) *gormlib.DB {
	tb.Helper()
	if os.Getenv("ENGRAM_TEST_DOCKER_MIGRATIONS") != "1" {
		tb.Skip("set ENGRAM_TEST_DOCKER_MIGRATIONS=1 to run disposable Docker migration tests; coverage not exercised")
	}
	var workDeadline time.Time
	switch current := tb.(type) {
	case *testing.T:
		testDeadline, ok := current.Deadline()
		if !ok {
			tb.Fatal("Docker migration fixture requires a finite test deadline")
		}
		workDeadline = testDeadline.Add(-90 * time.Second)
	case *testing.B:
		// Go 1.26 stops the testing alarm before benchmarks; this is a fixture budget, not -timeout.
		workDeadline = time.Now().Add(5 * time.Minute)
	default:
		tb.Fatal("Docker migration fixture requires a test or benchmark budget")
	}
	if time.Until(workDeadline) < 30*time.Second {
		tb.Fatal("insufficient test time for Docker fixture and cleanup reserve")
	}
	workCtx, workCancel := context.WithDeadline(context.Background(), workDeadline)
	tb.Cleanup(workCancel)
	if _, err := exec.LookPath("docker"); err != nil {
		tb.Fatalf("Docker fixture explicitly requested but docker is unavailable: %v", err)
	}
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		ctx, cancel := context.WithTimeout(workCtx, 20*time.Second)
		out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		cancel()
		if err != nil {
			tb.Fatalf("resolve Docker endpoint: %v", err)
		}
		endpoint = strings.TrimSpace(string(out))
	}
	switch endpoint {
	case "unix:///var/run/docker.sock", "npipe:////./pipe/docker_engine", "npipe:////./pipe/dockerDesktopLinuxEngine":
	default:
		tb.Fatalf("Docker fixture requires an explicitly local socket or named pipe; endpoint rejected")
	}
	select {
	case dockerMigrationSlot <- struct{}{}:
	case <-workCtx.Done():
		tb.Fatal("Docker fixture work budget expired waiting for the creation slot")
	}
	tb.Cleanup(func() { <-dockerMigrationSlot })
	if dockerMigrationUnreconciled {
		tb.Fatal("previous Docker fixture creation or cleanup is unreconciled; refusing another container")
	}
	ctx, cancel := context.WithTimeout(workCtx, 20*time.Second)
	err := fixtureDockerCommand(ctx, endpoint, "info", "--format", "{{.ID}}").Run()
	cancel()
	if err != nil {
		tb.Fatalf("local Docker daemon unavailable for explicitly requested fixture: %v", err)
	}

	name := "engram-migration-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := strings.ReplaceAll(uuid.NewString(), "-", "")
	if time.Until(workDeadline) < 30*time.Second {
		tb.Fatal("insufficient test time before Docker fixture creation")
	}
	dockerMigrationUnreconciled = true
	ctx, cancel = context.WithTimeout(workCtx, 3*time.Minute)
	out, err := fixtureDockerCommand(ctx, endpoint, "run", "--detach", "--pull=missing",
		"--name", name, "--memory", "512m", "--cpus", "1", "--pids-limit", "128",
		"--publish", "127.0.0.1::5432", "--tmpfs", "/var/lib/postgresql/data:rw,size=512m",
		"--env", "POSTGRES_USER=engram", "--env", "POSTGRES_DB=engram",
		"--env", "POSTGRES_PASSWORD="+password, "pgvector/pgvector:pg17").Output()
	cancel()
	if err != nil {
		tb.Fatalf("start disposable PostgreSQL container %s: %v; creation unreconciled", name, err)
	}
	id := strings.TrimSpace(string(out))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) {
		tb.Fatalf("Docker returned invalid container identity for %s; creation unreconciled", name)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := fixtureDockerCommand(ctx, endpoint, "rm", "--force", "--volumes", id).CombinedOutput(); err != nil {
			tb.Errorf("remove owned PostgreSQL container %s; cleanup unreconciled: %v: %s", id, err, output)
			return
		}
		dockerMigrationUnreconciled = false
	})
	ctx, cancel = context.WithTimeout(workCtx, 20*time.Second)
	portOutput, err := fixtureDockerCommand(ctx, endpoint, "port", id, "5432/tcp").Output()
	cancel()
	if err != nil {
		tb.Fatalf("read owned PostgreSQL port: %v", err)
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(string(portOutput)))
	if err != nil || host != "127.0.0.1" {
		tb.Fatal("owned PostgreSQL did not bind one loopback port")
	}
	dsn := fmt.Sprintf("host=127.0.0.1 port=%s user=engram password=%s dbname=engram sslmode=disable connect_timeout=2 options='-c statement_timeout=60000 -c lock_timeout=5000'", port, password)
	db, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		tb.Fatalf("open disposable PostgreSQL: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatalf("obtain disposable PostgreSQL pool: %v", err)
	}
	tb.Cleanup(func() {
		closed := make(chan error, 1)
		go func() { closed <- sqlDB.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				tb.Errorf("close disposable PostgreSQL pool: %v", err)
			}
		case <-time.After(20 * time.Second):
			tb.Error("close disposable PostgreSQL pool timed out")
		}
	})
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel = context.WithTimeout(workCtx, 2*time.Second)
		err = sqlDB.PingContext(ctx)
		cancel()
		if err == nil {
			break
		}
		if workCtx.Err() != nil {
			tb.Fatal("Docker fixture work budget expired waiting for PostgreSQL")
		}
		if time.Now().After(deadline) {
			tb.Fatalf("disposable PostgreSQL did not become ready: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	marker := strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel = context.WithTimeout(workCtx, 20*time.Second)
	err = fixtureDockerCommand(ctx, endpoint, "exec", id, "psql", "-U", "engram", "-d", "engram", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE fixture_identity (marker text NOT NULL); INSERT INTO fixture_identity VALUES ('"+marker+"')").Run()
	cancel()
	if err != nil {
		tb.Fatalf("write container identity marker: %v", err)
	}
	ctx, cancel = context.WithTimeout(workCtx, 10*time.Second)
	fixtureConn, err := sqlDB.Conn(ctx)
	cancel()
	if err != nil {
		tb.Fatalf("pin disposable PostgreSQL connection: %v", err)
	}
	tb.Cleanup(func() {
		closed := make(chan error, 1)
		go func() { closed <- fixtureConn.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				tb.Errorf("close pinned PostgreSQL connection: %v", err)
			}
		case <-time.After(20 * time.Second):
			tb.Error("close pinned PostgreSQL connection timed out")
		}
	})
	ctx, cancel = context.WithTimeout(workCtx, 10*time.Second)
	var actual string
	err = fixtureConn.QueryRowContext(ctx, `SELECT marker FROM fixture_identity`).Scan(&actual)
	cancel()
	if err != nil || actual != marker {
		tb.Fatal("SQL connection does not match the owned container; migrations refused")
	}
	db, err = gormlib.Open(postgres.New(postgres.Config{Conn: fixtureConn}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		tb.Fatalf("bind verified PostgreSQL connection: %v", err)
	}
	fixtureCtx, fixtureCancel := context.WithTimeout(workCtx, 5*time.Minute)
	tb.Cleanup(fixtureCancel)
	db = db.WithContext(fixtureCtx)
	if err := runMigrations(db); err != nil {
		tb.Fatalf("run migrations in disposable PostgreSQL: %v", err)
	}
	return db
}
