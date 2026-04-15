package grpcserver

import (
	"context"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	pb "github.com/thebtf/engram/proto/engram/v1"
)

// testGRPCSyncDB opens a real postgres test DB and creates a minimal projects schema.
// Tests skip when DATABASE_DSN is not set.
func testGRPCSyncDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping gRPC SyncProjectState integration test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}

	// Minimal schema.
	ddl := `CREATE TABLE IF NOT EXISTS projects (
		id             TEXT PRIMARY KEY,
		git_remote     TEXT,
		relative_path  TEXT,
		display_name   TEXT,
		legacy_ids     TEXT[],
		created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at     TIMESTAMPTZ NULL,
		last_heartbeat TIMESTAMPTZ DEFAULT NOW()
	)`
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}

	sqlDB, _ := db.DB()
	return db, func() { sqlDB.Close() }
}

// syncServer returns a minimal *Server wired with the given db.
func syncServer(t *testing.T, db *gorm.DB) *Server {
	t.Helper()
	// handler is nil — SyncProjectState does not use the MCP handler.
	srv := &Server{db: db}
	return srv
}

func TestSyncProjectState_TooManyIds(t *testing.T) {
	t.Parallel()

	srv := &Server{}
	ids := make([]string, maxSyncProjectIDs+1)
	for i := range ids {
		ids[i] = "proj"
	}
	req := &pb.SyncProjectStateRequest{
		ClientId:        "daemon-1",
		LocalProjectIds: ids,
	}
	_, err := srv.SyncProjectState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for too many IDs")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSyncProjectState_EmptyClientId(t *testing.T) {
	t.Parallel()

	srv := &Server{}
	req := &pb.SyncProjectStateRequest{
		ClientId: "",
	}
	_, err := srv.SyncProjectState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty client_id")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestSyncProjectState_HappyPath(t *testing.T) {
	t.Parallel()

	db, cleanup := testGRPCSyncDB(t)
	defer cleanup()

	projectID := "sync-happy-" + t.Name()
	if err := db.Exec("INSERT INTO projects (id) VALUES (?) ON CONFLICT DO NOTHING", projectID).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	srv := syncServer(t, db)
	req := &pb.SyncProjectStateRequest{
		ClientId:        "daemon-test-1",
		LocalProjectIds: []string{projectID},
	}
	resp, err := srv.SyncProjectState(context.Background(), req)
	if err != nil {
		t.Fatalf("SyncProjectState: %v", err)
	}
	if resp.ServerTimeUnixMs == 0 {
		t.Error("expected non-zero ServerTimeUnixMs")
	}
	// A live project must not appear in removed.
	for _, r := range resp.GetRemoved() {
		if r == projectID {
			t.Errorf("live project should not be in removed, but found it")
		}
	}
}

func TestSyncProjectState_RemovedDetected(t *testing.T) {
	t.Parallel()

	db, cleanup := testGRPCSyncDB(t)
	defer cleanup()

	projectID := "sync-removed-" + t.Name()
	now := time.Now().UTC()
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		projectID, now,
	).Error; err != nil {
		t.Fatalf("insert soft-deleted project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	srv := syncServer(t, db)
	req := &pb.SyncProjectStateRequest{
		ClientId:        "daemon-test-2",
		LocalProjectIds: []string{projectID},
	}
	resp, err := srv.SyncProjectState(context.Background(), req)
	if err != nil {
		t.Fatalf("SyncProjectState: %v", err)
	}

	found := false
	for _, r := range resp.GetRemoved() {
		if r == projectID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected soft-deleted project %s in removed[], got %v", projectID, resp.GetRemoved())
	}
}

func TestSyncProjectState_HeartbeatUpdated(t *testing.T) {
	t.Parallel()

	db, cleanup := testGRPCSyncDB(t)
	defer cleanup()

	projectID := "sync-heartbeat-" + t.Name()
	// Insert with a very old heartbeat.
	oldTime := time.Now().Add(-48 * time.Hour).UTC()
	if err := db.Exec(
		"INSERT INTO projects (id, last_heartbeat) VALUES (?, ?) ON CONFLICT DO NOTHING",
		projectID, oldTime,
	).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	srv := syncServer(t, db)
	before := time.Now().UTC()
	req := &pb.SyncProjectStateRequest{
		ClientId:        "daemon-test-3",
		LocalProjectIds: []string{projectID},
	}
	if _, err := srv.SyncProjectState(context.Background(), req); err != nil {
		t.Fatalf("SyncProjectState: %v", err)
	}

	var newHB *time.Time
	row := db.Raw("SELECT last_heartbeat FROM projects WHERE id = ?", projectID).Row()
	if err := row.Scan(&newHB); err != nil {
		t.Fatalf("scan last_heartbeat: %v", err)
	}
	if newHB == nil || newHB.Before(before) {
		t.Errorf("expected last_heartbeat to be updated to >= %v, got %v", before, newHB)
	}
}
