package worker

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/pkg/cognitive"
)

type statePlane interface {
	cognitive.StatePlane
}

func (s *Service) handleGetStateSession(w http.ResponseWriter, r *http.Request) {
	store := s.stateStore
	if store == nil {
		http.Error(w, "state store not available", http.StatusServiceUnavailable)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	state, err := store.ReadSessionState(r.Context(), sessionID)
	if err != nil {
		writeStateReadError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"source":     cognitive.StatePacketSourceNative,
		"session_id": sessionID,
		"state":      state,
	})
}

func (s *Service) handleGetStateProject(w http.ResponseWriter, r *http.Request) {
	store := s.stateStore
	if store == nil {
		http.Error(w, "state store not available", http.StatusServiceUnavailable)
		return
	}
	project := chi.URLParam(r, "project")
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	state, err := store.ReadProjectState(r.Context(), project)
	if err != nil {
		writeStateReadError(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"source":  cognitive.StatePacketSourceNative,
		"project": project,
		"state":   state,
	})
}

func (s *Service) handleGetStateResume(w http.ResponseWriter, r *http.Request) {
	store := s.stateStore
	if store == nil {
		http.Error(w, "state store not available", http.StatusServiceUnavailable)
		return
	}
	query := r.URL.Query()
	sessionID := query.Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	allowFallback := false
	if raw := query.Get("allow_filesystem_fallback"); raw != "" {
		var err error
		allowFallback, err = strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "allow_filesystem_fallback must be a boolean", http.StatusBadRequest)
			return
		}
	}
	packet, err := store.ReadResumePacket(r.Context(), cognitive.ResumePacketRequest{
		Project:                 query.Get("project"),
		SessionID:               sessionID,
		GoalID:                  query.Get("goal_id"),
		TaskID:                  query.Get("task_id"),
		AllowFilesystemFallback: allowFallback,
	})
	if err != nil {
		writeStateReadError(w, err)
		return
	}
	writeJSON(w, packet)
}

func writeStateReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, gormlib.ErrRecordNotFound) {
		http.Error(w, "state not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
