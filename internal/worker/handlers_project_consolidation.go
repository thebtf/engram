package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	"github.com/thebtf/engram/internal/auth"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Existing data-admin authorization protects migration data/effects, not project
// resolution. No principal ownership ceremony or V3 descriptor is introduced.
func (s *Service) projectConsolidation(r *http.Request, restore bool) (*dbgorm.ProjectConsolidationStore, func(), error) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok || !identity.IsAdmin() {
		return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_AUTH_REQUIRED"}
	}
	if s.store == nil || s.store.DB == nil {
		return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_INVENTORY_INCOMPLETE"}
	}
	vault, err := vaultcrypto.OpenExistingVault(s.config)
	if err != nil {
		return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_BACKUP_MISSING"}
	}
	var target *gormlib.DB
	cleanup := func() {}
	if restore {
		dsn := os.Getenv("ENGRAM_CONSOLIDATION_RESTORE_DSN")
		if dsn == "" {
			return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_ISOLATED_TARGET_MISSING"}
		}
		target, err = gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_ISOLATED_TARGET_MISSING"}
		}
		pool, err := target.DB()
		if err != nil {
			return nil, nil, &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_ISOLATED_TARGET_MISSING"}
		}
		pool.SetMaxOpenConns(2)
		cleanup = func() { _ = pool.Close() }
	}
	return dbgorm.NewProjectConsolidationStore(s.store.DB, vault, target), cleanup, nil
}

func consolidationHTTPError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var refusal *dbgorm.ConsolidationError
	if !errors.As(err, &refusal) {
		refusal = &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_INVENTORY_INCOMPLETE"}
	}
	status := http.StatusConflict
	if refusal.Code == "PROJECT_ADOPTION_AUTH_REQUIRED" {
		status = http.StatusForbidden
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": refusal.Code, "message": "Migration held; retain data and satisfy the named snapshot, mapping, secret or isolated-target prerequisite"})
}

func decodeConsolidationRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 56<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid project consolidation request", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Service) handleProjectConsolidationCensus(w http.ResponseWriter, r *http.Request) {
	store, cleanup, err := s.projectConsolidation(r, false)
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	defer cleanup()
	census, err := store.Census(r.Context(), r.URL.Query().Get("source_version"))
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, census)
}

func (s *Service) handleProjectConsolidationPreflight(w http.ResponseWriter, r *http.Request) {
	var request dbgorm.ConsolidationPreflightRequest
	if !decodeConsolidationRequest(w, r, &request) {
		return
	}
	store, cleanup, err := s.projectConsolidation(r, false)
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	defer cleanup()
	plan, err := store.Preflight(r.Context(), request)
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, plan)
}

type projectPlanRequest struct {
	PlanReference   string `json:"plan_reference"`
	PlanFingerprint string `json:"plan_fingerprint"`
}

type consolidationHTTPStream struct {
	http.ResponseWriter
	started bool
}

func (stream *consolidationHTTPStream) Write(raw []byte) (int, error) {
	if !stream.started {
		stream.Header().Set("Cache-Control", "no-store")
		stream.Header().Set("Content-Type", "application/octet-stream")
		stream.Header().Set("Content-Disposition", "attachment; filename=engram-project-backup.sealed")
		stream.Header().Set("Trailer", "X-Engram-Backup-Error")
		stream.started = true
	}
	return stream.ResponseWriter.Write(raw)
}

func (s *Service) handleProjectConsolidationBackup(w http.ResponseWriter, r *http.Request) {
	var request projectPlanRequest
	if !decodeConsolidationRequest(w, r, &request) {
		return
	}
	store, cleanup, err := s.projectConsolidation(r, false)
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	defer cleanup()
	stream := &consolidationHTTPStream{ResponseWriter: w}
	if err := store.Backup(r.Context(), request.PlanReference, request.PlanFingerprint, stream); err != nil {
		if !stream.started {
			consolidationHTTPError(w, err)
			return
		}
		var refusal *dbgorm.ConsolidationError
		code := "PROJECT_ADOPTION_BACKUP_INTERRUPTED"
		if errors.As(err, &refusal) {
			code = refusal.Code
		}
		w.Header().Set("X-Engram-Backup-Error", code)
	}
}

type projectConsolidationVerifier func(context.Context, string, string, io.Reader) (string, error)

func (s *Service) verifyProjectConsolidationBackup(r *http.Request, request projectPlanRequest, src io.Reader) (string, error) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok || !identity.IsAdmin() {
		return "", &dbgorm.ConsolidationError{Code: "PROJECT_ADOPTION_AUTH_REQUIRED"}
	}
	if s.projectConsolidationVerifierSeam != nil {
		return s.projectConsolidationVerifierSeam(r.Context(), request.PlanReference, request.PlanFingerprint, src)
	}
	store, cleanup, err := s.projectConsolidation(r, true)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return store.VerifyBackup(r.Context(), request.PlanReference, request.PlanFingerprint, src)
}

func consolidationMultipartError(w http.ResponseWriter, err error, message string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		consolidationHTTPError(w, err)
		return
	}
	http.Error(w, message, http.StatusBadRequest)
}

type consolidationMultipartStream struct {
	part     io.Reader
	reader   *multipart.Reader
	readErr  error
	finished bool
}

func (stream *consolidationMultipartStream) Read(raw []byte) (int, error) {
	if stream.finished {
		return 0, io.EOF
	}
	n, err := stream.part.Read(raw)
	if err == io.EOF {
		_, next := stream.reader.NextPart()
		if next != io.EOF {
			if next == nil {
				next = errors.New("unexpected backup part")
			}
			stream.readErr = next
			return n, next
		}
		stream.finished = true
	}
	if err != nil && err != io.EOF {
		stream.readErr = err
	}
	return n, err
}

func (s *Service) handleProjectConsolidationVerifyBackup(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	reader, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "ordered multipart backup required", http.StatusBadRequest)
		return
	}
	var request projectPlanRequest
	for _, field := range []struct {
		name   string
		target *string
		size   int64
	}{{"plan_reference", &request.PlanReference, 32 << 20}, {"plan_fingerprint", &request.PlanFingerprint, 256}} {
		part, err := reader.NextPart()
		if err != nil || part.FormName() != field.name || part.FileName() != "" {
			consolidationMultipartError(w, err, "invalid backup metadata order")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(part, field.size+1))
		if err != nil || int64(len(raw)) > field.size {
			consolidationMultipartError(w, err, "backup metadata too large")
			return
		}
		if len(raw) == 0 {
			http.Error(w, "invalid backup metadata", http.StatusBadRequest)
			return
		}
		if err := part.Close(); err != nil {
			consolidationMultipartError(w, err, "invalid backup metadata")
			return
		}
		*field.target = string(raw)
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "sealed_export" {
		consolidationMultipartError(w, err, "sealed_export part required")
		return
	}
	stream := &consolidationMultipartStream{part: part, reader: reader}
	reference, err := s.verifyProjectConsolidationBackup(r, request, stream)
	// Let the registered Timeout middleware own the response after cancellation.
	if r.Context().Err() != nil {
		return
	}
	// Frame validation translates read errors; retain the transport refusal.
	var tooLarge *http.MaxBytesError
	if errors.As(stream.readErr, &tooLarge) {
		err = stream.readErr
	}
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	if err := part.Close(); err != nil {
		consolidationHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"backup_reference": reference, "restore": "verified_isolated_database"})
}

func (s *Service) handleProjectConsolidationDryRun(w http.ResponseWriter, r *http.Request) {
	s.handleProjectConsolidationExecute(w, r, true)
}

func (s *Service) handleProjectConsolidationApply(w http.ResponseWriter, r *http.Request) {
	s.handleProjectConsolidationExecute(w, r, false)
}

func (s *Service) handleProjectConsolidationExecute(w http.ResponseWriter, r *http.Request, dryRun bool) {
	var request dbgorm.ConsolidationApplyRequest
	if !decodeConsolidationRequest(w, r, &request) {
		return
	}
	store, cleanup, err := s.projectConsolidation(r, true)
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	defer cleanup()
	var receipt dbgorm.ConsolidationReceipt
	if dryRun {
		receipt, err = store.DryRun(r.Context(), request)
	} else {
		receipt, err = store.Apply(r.Context(), request)
	}
	if err != nil {
		consolidationHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, receipt)
}
