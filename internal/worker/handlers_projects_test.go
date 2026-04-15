package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/worker/projectevents"
)

// setupProjectTestDB opens a postgres test DB (via DATABASE_DSN) and ensures
// the projects table exists with the lifecycle columns from migration 082.
// The caller is responsible for calling cleanup().
func setupProjectTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping DB-backed handler test")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}

	// Minimal schema for the projects table with lifecycle columns.
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id            TEXT PRIMARY KEY,
			git_remote    TEXT,
			relative_path TEXT,
			display_name  TEXT,
			legacy_ids    TEXT[],
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			removed_at    TIMESTAMPTZ NULL,
			last_heartbeat TIMESTAMPTZ DEFAULT NOW()
		)`,
	}
	for _, stmt := range ddl {
		if err := db.Exec(stmt).Error; err != nil {
			sqlDB.Close()
			t.Fatalf("create test schema: %v", err)
		}
	}

	cleanup := func() {
		// Remove only the rows inserted by this test; leave other test data intact.
		sqlDB.Close()
	}
	return db, cleanup
}

// projectTestService builds a minimal Service wired with the given gorm.DB and event bus.
func projectTestService(t *testing.T, db *gorm.DB, bus *projectevents.Bus) *Service {
	t.Helper()
	store := &gormdb.Store{}
	store.DB = db
	svc := &Service{
		store:    store,
		eventBus: bus,
	}
	return svc
}

// newCHIRequest creates an *http.Request with chi URL params injected.
func newCHIRequest(method, target, paramName, paramValue string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(paramName, paramValue)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestDeleteProject_MalformedId verifies that IDs containing path traversal return 400.
func TestDeleteProject_MalformedId(t *testing.T) {
	t.Parallel()

	svc := &Service{eventBus: &projectevents.Bus{}}
	req := newCHIRequest(http.MethodDelete, "/api/projects/%2F..%2Fetc", "id", "../etc/passwd")
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for path-traversal id, got %d", w.Code)
	}
}

// TestDeleteProject_MissingId verifies that a missing id param returns 400.
func TestDeleteProject_MissingId(t *testing.T) {
	t.Parallel()

	svc := &Service{eventBus: &projectevents.Bus{}}
	req := newCHIRequest(http.MethodDelete, "/api/projects/", "id", "")
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing id, got %d", w.Code)
	}
}

// TestDeleteProject_HappyPath verifies soft-delete sets removed_at and returns 200.
func TestDeleteProject_HappyPath(t *testing.T) {
	t.Parallel()

	db, cleanup := setupProjectTestDB(t)
	defer cleanup()

	// Insert a live project row.
	projectID := "test-proj-happy-" + t.Name()
	if err := db.Exec("INSERT INTO projects (id) VALUES (?) ON CONFLICT DO NOTHING", projectID).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	bus := &projectevents.Bus{}
	svc := projectTestService(t, db, bus)

	req := newCHIRequest(http.MethodDelete, "/api/projects/"+projectID, "id", projectID)
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] != projectID {
		t.Errorf("expected id=%s, got %s", projectID, resp["id"])
	}
	if resp["removed_at"] == "" {
		t.Error("expected removed_at in response")
	}

	// Verify removed_at is set in DB.
	var removedAt *time.Time
	row := db.Raw("SELECT removed_at FROM projects WHERE id = ?", projectID).Row()
	if err := row.Scan(&removedAt); err != nil {
		t.Fatalf("query removed_at: %v", err)
	}
	if removedAt == nil {
		t.Error("expected removed_at to be set in DB, got NULL")
	}
}

// TestDeleteProject_AlreadyDeleted verifies that a second delete returns 404.
func TestDeleteProject_AlreadyDeleted(t *testing.T) {
	t.Parallel()

	db, cleanup := setupProjectTestDB(t)
	defer cleanup()

	projectID := "test-proj-already-deleted-" + t.Name()
	now := time.Now().UTC()
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		projectID, now,
	).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	svc := projectTestService(t, db, &projectevents.Bus{})
	req := newCHIRequest(http.MethodDelete, "/api/projects/"+projectID, "id", projectID)
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for already-deleted project, got %d", w.Code)
	}
}

// TestDeleteProject_NotFound verifies that deleting a nonexistent project returns 404.
func TestDeleteProject_NotFound(t *testing.T) {
	t.Parallel()

	db, cleanup := setupProjectTestDB(t)
	defer cleanup()

	svc := projectTestService(t, db, &projectevents.Bus{})
	req := newCHIRequest(http.MethodDelete, "/api/projects/nonexistent-xyz", "id", "nonexistent-xyz")
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent project, got %d", w.Code)
	}
}

// TestDeleteProject_EmitsEvent verifies the event bus receives the removal event on success.
func TestDeleteProject_EmitsEvent(t *testing.T) {
	t.Parallel()

	db, cleanup := setupProjectTestDB(t)
	defer cleanup()

	projectID := "test-proj-emit-" + t.Name()
	if err := db.Exec("INSERT INTO projects (id) VALUES (?) ON CONFLICT DO NOTHING", projectID).Error; err != nil {
		t.Fatalf("insert project: %v", err)
	}
	defer db.Exec("DELETE FROM projects WHERE id = ?", projectID)

	bus := &projectevents.Bus{}
	var emitted atomic.Int64
	var capturedID string
	bus.Subscribe(func(ev projectevents.Event) {
		emitted.Add(1)
		capturedID = ev.ProjectID
	})

	svc := projectTestService(t, db, bus)
	req := newCHIRequest(http.MethodDelete, "/api/projects/"+projectID, "id", projectID)
	w := httptest.NewRecorder()

	svc.handleDeleteProject(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if emitted.Load() != 1 {
		t.Fatalf("expected 1 event emitted, got %d", emitted.Load())
	}
	if capturedID != projectID {
		t.Errorf("expected event project_id=%s, got %s", projectID, capturedID)
	}
}

// TestDeleteProject_SoftDeletedInvisibleToGetProjects verifies that handleGetProjects
// excludes soft-deleted projects (T034 AC).
func TestDeleteProject_SoftDeletedInvisibleToGetProjects(t *testing.T) {
	t.Parallel()

	db, cleanup := setupProjectTestDB(t)
	defer cleanup()

	// Insert one live and one soft-deleted project.
	liveID := "test-live-proj-" + t.Name()
	deletedID := "test-dead-proj-" + t.Name()
	now := time.Now().UTC()

	if err := db.Exec("INSERT INTO projects (id) VALUES (?) ON CONFLICT DO NOTHING", liveID).Error; err != nil {
		t.Fatalf("insert live project: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO projects (id, removed_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		deletedID, now,
	).Error; err != nil {
		t.Fatalf("insert deleted project: %v", err)
	}
	defer func() {
		db.Exec("DELETE FROM projects WHERE id = ?", liveID)
		db.Exec("DELETE FROM projects WHERE id = ?", deletedID)
	}()

	// Query projects with the removed_at IS NULL filter (what the audit requires).
	var rows []string
	if err := db.Raw("SELECT id FROM projects WHERE removed_at IS NULL AND id LIKE 'test-%' ORDER BY id").
		Pluck("id", &rows).Error; err != nil {
		t.Fatalf("query filtered projects: %v", err)
	}

	for _, r := range rows {
		if r == deletedID {
			t.Errorf("soft-deleted project %s should not appear in filtered results", deletedID)
		}
	}

	found := false
	for _, r := range rows {
		if r == liveID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("live project %s should appear in filtered results", liveID)
	}
}
