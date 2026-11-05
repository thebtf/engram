package worker

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/projectidentity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const projectLabelBodyLimit = 4096

// projectLabelPatchSchema describes the closed JSON envelope parsed below.
type projectLabelPatchSchema struct {
	DisplayName         *string `json:"display_name" binding:"required" extensions:"x-nullable"`
	ExpectedDisplayName *string `json:"expected_display_name" binding:"required" extensions:"x-nullable"`
}

// handlePatchProjectLabel godoc
// @Summary Conditionally update a project's stored display label
// @Description Requires an operator/admin identity, a canonical project UUID, and both display_name and expected_display_name as string or null. The expected value is the exact stored value from the project catalog, not its presentation name.
// @Tags Projects
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Canonical project UUID"
// @Param request body projectLabelPatchSchema true "display_name and expected_display_name"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {string} string "invalid request"
// @Failure 401 {string} string "unauthorized"
// @Failure 403 {string} string "operator access required"
// @Failure 404 {string} string "project not found or inactive"
// @Failure 409 {string} string "stored label changed"
// @Failure 413 {string} string "request body too large"
// @Failure 500 {string} string "internal error"
// @Router /api/projects/{id} [patch]
func (s *Service) handlePatchProjectLabel(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !identity.IsAdmin() || (identity.Source != auth.SourceMaster && identity.Source != auth.SourceSession && identity.Source != auth.SourceAuthDisabled) {
		http.Error(w, "operator access required", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !projectidentity.ValidProjectID(id) {
		http.Error(w, "invalid project id", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, projectLabelBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request", http.StatusBadRequest)
		}
		return
	}
	newLabel, expected, ok := parseProjectLabelPatch(body)
	if !ok {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if s.store == nil || s.store.DB == nil {
		http.Error(w, "project storage unavailable", http.StatusServiceUnavailable)
		return
	}

	notFound := errors.New("project not found or inactive")
	stale := errors.New("stored label changed")
	err = s.store.DB.WithContext(r.Context()).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var current sql.NullString
		err := tx.Raw(`SELECT display_name FROM projects WHERE id = ? AND removed_at IS NULL AND (identity_status IS NULL OR identity_status = 'active') FOR UPDATE`, id).Row().Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return err
		}
		if current != expected {
			return stale
		}
		result := tx.Exec(`UPDATE projects SET display_name = ? WHERE id = ? AND removed_at IS NULL AND (identity_status IS NULL OR identity_status = 'active') AND display_name IS NOT DISTINCT FROM ?`, newLabel, id, expected)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return stale
		}
		return nil
	})
	switch {
	case errors.Is(err, notFound):
		http.Error(w, "project not found or inactive", http.StatusNotFound)
	case errors.Is(err, stale):
		http.Error(w, "stored label changed", http.StatusConflict)
	case err != nil:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	default:
		writeJSON(w, map[string]any{"project_id": id, "display_name": nullableProjectLabel(newLabel)})
	}
}

func nullableProjectLabel(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func parseProjectLabelPatch(body []byte) (sql.NullString, sql.NullString, bool) {
	var newLabel, expected sql.NullString
	if !utf8.Valid(body) {
		return newLabel, expected, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return newLabel, expected, false
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] || (key != "display_name" && key != "expected_display_name") {
			return newLabel, expected, false
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return newLabel, expected, false
		}
		value := sql.NullString{}
		if !bytes.Equal(raw, []byte("null")) {
			if json.Unmarshal(raw, &value.String) != nil || !utf8.ValidString(value.String) || utf8.RuneCountInString(value.String) > 255 || strings.IndexFunc(value.String, unicode.IsControl) >= 0 {
				return newLabel, expected, false
			}
			value.Valid = true
		}
		if key == "display_name" {
			newLabel = value
		} else {
			expected = value
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || !seen["display_name"] || !seen["expected_display_name"] {
		return newLabel, expected, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return newLabel, expected, false
	}
	return newLabel, expected, true
}
