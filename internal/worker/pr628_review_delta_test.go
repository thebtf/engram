package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/db/projectscope"
)

func reviewDeltaStore(t *testing.T) *gormdb.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("dedicated migration186 PostgreSQL fixture required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid fixture DSN")
	}
	const prefix, suffix = "engram_migration186_scope_test_", "_source"
	require.True(t, cfg.Host == "127.0.0.1" || cfg.Host == "localhost" || cfg.Host == "::1")
	require.True(t, strings.HasPrefix(cfg.Database, prefix) && strings.HasSuffix(cfg.Database, suffix) && len(cfg.Database) > len(prefix)+len(suffix))
	require.True(t, cfg.RuntimeParams["search_path"] == "" || cfg.RuntimeParams["search_path"] == "public")
	require.Empty(t, cfg.Fallbacks)
	return openWorkerUCIContextCompositionStore(t)
}

func TestResolveUUIDAgreesWithCanonicalReaders(t *testing.T) {
	backing := reviewDeltaStore(t)
	db := backing.DB
	service := &Service{store: backing, vault: newTestVault(t), sessionStore: gormdb.NewSessionStore(backing), memoryStore: gormdb.NewMemoryStore(backing)}
	router := chi.NewRouter()
	router.Get("/api/projects/resolve", service.handleResolveLogicalProject)
	router.Get("/api/memories/{id}", service.handleGetMemoryByID)
	admin := auth.SessionForBrowserUser("admin", 41)
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: documentSelectionTestSession})
		req = req.WithContext(auth.WithIdentity(req.Context(), admin))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, mode := range []string{"direct", "terminal_self", "chain", "session_conflict"} {
		t.Run(mode, func(t *testing.T) {
			a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
			for _, id := range []string{a, b, c} {
				require.NoError(t, db.Create(&gormdb.Project{ID: id}).Error)
			}
			memory := gormdb.Memory{Project: b, Content: "scope witness", Status: "active", PrivacyScope: "project", Version: 1}
			require.NoError(t, db.Create(&memory).Error)
			require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'fixture')`, a, b).Error)
			switch mode {
			case "terminal_self":
				require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'fixture')`, b, b).Error)
			case "chain":
				require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, '', ?, 'fixture')`, b, c).Error)
			case "session_conflict":
				require.NoError(t, db.Exec(`INSERT INTO project_consolidation_redirects(selector,session_id,project_id,plan_fingerprint) VALUES (?, ?, ?, 'fixture')`, a, documentSelectionTestSession, c).Error)
			}
			var canonical string
			require.NoError(t, db.Raw(`SELECT `+projectscope.CanonicalReadProjectSQL("?"), a, a).Scan(&canonical).Error)
			rec := call("/api/projects/resolve?legacy_id=" + a)
			if rec.Code == http.StatusNotFound && canonical == a {
				return
			}
			require.Equal(t, http.StatusOK, rec.Code)
			var response struct {
				ProjectID string `json:"project_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			if response.ProjectID != canonical {
				t.Errorf("resolver=%s canonical_reader=%s", response.ProjectID, canonical)
			}
			read := call(fmt.Sprintf("/api/memories/%d?project=%s", memory.ID, a))
			want := http.StatusNotFound
			if canonical == b {
				want = http.StatusOK
			}
			require.Equal(t, want, read.Code)
		})
	}
}
