package worker

import (
	"encoding/json"
	"io"
	"net/http"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
)

// Authentication remains at the ordinary /api middleware boundary.
func (s *Service) handleProjectBootstrap(w http.ResponseWriter, r *http.Request) {
	var input projectidentity.BootstrapRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"code": "PROJECT_LOCATOR_INVALID"})
		return
	}
	if s.store == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"code": "PROJECT_RESOLUTION_UNAVAILABLE"})
		return
	}
	project, err := gormdb.BootstrapLogicalProject(r.Context(), s.store.DB, input)
	if err != nil {
		code := projectidentity.ProjectErrorCode(err)
		status := http.StatusServiceUnavailable
		if code == "PROJECT_LOCATOR_INVALID" || code == "PROJECT_IDENTITY_INVALID" {
			status = http.StatusBadRequest
		}
		if code == "PROJECT_IDENTITY_CONFLICT" || code == "PROJECT_MIGRATION_REQUIRED" {
			status = http.StatusConflict
		}
		writeJSONStatus(w, status, map[string]string{"code": code})
		return
	}
	writeJSON(w, project)
}
