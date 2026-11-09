package worker

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	"github.com/thebtf/engram/internal/projectidentity"
)

func writeIssueTaskError(w http.ResponseWriter, err error) {
	var failure *issues.Error
	if !errors.As(err, &failure) {
		failure = issues.Failure("UNAVAILABLE")
	}
	status := http.StatusServiceUnavailable
	switch failure.Code {
	case "INVALID_ARGUMENT", "PRIVACY_REJECTED":
		status = http.StatusBadRequest
	case "PAYLOAD_TOO_LARGE", "TRANSPORT_RECORD_TOO_LARGE":
		status = http.StatusRequestEntityTooLarge
	case "UNAUTHENTICATED":
		status = http.StatusUnauthorized
	case "FORBIDDEN":
		status = http.StatusForbidden
	case "ISSUE_NOT_FOUND":
		status = http.StatusNotFound
	case "ACTOR_CHANGED", "IDEMPOTENCY_CONFLICT", "REVISION_CONFLICT", "INVALID_TRANSITION", "DETAIL_PAGING_REQUIRED":
		status = http.StatusConflict
	case "REVISION_REQUIRED":
		status = http.StatusPreconditionRequired
	case "ISSUE_GONE":
		status = http.StatusGone
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": failure})
}

// Read the actual HTTP request ceiling without reserializing received JSON.
// Source extraction does not inspect expected_actor, numbers or private data;
// the application alone captures the actor and performs admitted DTO validation.
func readIssueTaskWrite(w http.ResponseWriter, r *http.Request) (json.RawMessage, issues.Scope, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, issues.Scope{}, issues.Failure("PAYLOAD_TOO_LARGE")
		}
		return nil, issues.Scope{}, issues.Failure("INVALID_ARGUMENT")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, issues.Scope{}, issues.Failure("INVALID_ARGUMENT")
	}
	var source string
	if encoded, present := fields["source_project"]; present {
		if json.Unmarshal(encoded, &source) != nil {
			return nil, issues.Scope{}, issues.Failure("INVALID_ARGUMENT")
		}
	}
	return raw, issues.Scope{SourceProject: source}, nil
}

func issueTaskQuery(r *http.Request) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, issues.Failure("INVALID_ARGUMENT")
	}
	for _, values := range query {
		if len(values) != 1 {
			return nil, issues.Failure("INVALID_ARGUMENT")
		}
	}
	return query, nil
}

func issueTaskInt(query url.Values, key string, fallback, minValue, maxValue int) (int, error) {
	if !query.Has(key) {
		return fallback, nil
	}
	raw := query.Get(key)
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, issues.Failure("INVALID_ARGUMENT")
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || raw == "" || value < minValue || value > maxValue {
		return 0, issues.Failure("INVALID_ARGUMENT")
	}
	return value, nil
}

func issueTaskProject(query url.Values, key string) (string, error) {
	if !query.Has(key) {
		return "", nil
	}
	project := query.Get(key)
	if !projectidentity.ValidProjectID(project) {
		return "", issues.Failure("INVALID_ARGUMENT")
	}
	return project, nil
}

func issueTaskReadIdentity(r *http.Request) error {
	_, err := issues.CaptureActor(r.Context())
	return err
}

func (s *Service) listIssueTasks(w http.ResponseWriter, r *http.Request) {
	if err := issueTaskReadIdentity(r); err != nil {
		writeIssueTaskError(w, err)
		return
	}
	query, err := issueTaskQuery(r)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params := gormdb.IssueTaskListParams{}
	params.TargetProject, err = issueTaskProject(query, "project")
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.SourceProject, err = issueTaskProject(query, "source_project")
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.Limit, err = issueTaskInt(query, "limit", 50, 1, 200)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.Offset, err = issueTaskInt(query, "offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.SourceAgent = query.Get("source_agent")
	params.SourceSession = query.Get("source_session")
	params.TargetSession = query.Get("target_session")
	params.ParticipantSession = query.Get("participant_session")
	for _, field := range []string{"source_session", "target_session", "participant_session"} {
		if query.Has(field) && query.Get(field) == "" {
			writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
			return
		}
	}
	params.Query = query.Get("q")
	params.Type = strings.ToLower(strings.TrimSpace(query.Get("type")))
	if params.Type != "" && params.Type != "bug" && params.Type != "feature" && params.Type != "improvement" && params.Type != "task" {
		writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
		return
	}
	if query.Has("status") {
		for _, value := range strings.Split(query.Get("status"), ",") {
			value = strings.TrimSpace(value)
			switch value {
			case "open", "acknowledged", "resolved", "reopened", "closed", "rejected":
			default:
				writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
				return
			}
			params.Statuses = append(params.Statuses, value)
		}
	}
	if query.Has("resolved_since") {
		ms, err := strconv.ParseInt(query.Get("resolved_since"), 10, 64)
		if err != nil {
			writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
			return
		}
		since := time.UnixMilli(ms)
		params.ResolvedSince = &since
	}
	if query.Has("owner_label") {
		label := query.Get("owner_label")
		params.OwnerLabel = &label
	}
	if query.Has("recipient_label") {
		label := query.Get("recipient_label")
		params.RecipientLabel = &label
	}
	result, err := s.issueStore.ListTaskIssues(r.Context(), params)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	type summary struct {
		gormdb.IssueTaskSummary
		ProjectOrigin issueProjectOrigin `json:"project_origin"`
	}
	items := make([]summary, len(result.Issues))
	names := make(map[string]string)
	for index, row := range result.Issues {
		items[index] = summary{row, issueProjectOrigin{row.SourceProject, row.TargetProject}}
		for _, project := range []*string{row.SourceProjectID, row.TargetProjectID} {
			if project != nil {
				if _, present := names[*project]; !present {
					names[*project] = s.getProjectDisplayName(r.Context(), *project)
				}
			}
		}
	}
	writeJSON(w, map[string]any{"issues": items, "total": result.Total, "limit": result.Limit, "offset": result.Offset, "next_offset": result.NextOffset, "project_names": names})
}

func (s *Service) getIssueTask(w http.ResponseWriter, r *http.Request) {
	if err := issueTaskReadIdentity(r); err != nil {
		writeIssueTaskError(w, err)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
		return
	}
	query, err := issueTaskQuery(r)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params := gormdb.IssueTaskDetailParams{}
	params.ReadProject, err = issueTaskProject(query, "project")
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	if query.Has("detail_mode") && query.Get("detail_mode") != "paged" {
		writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
		return
	}
	params.Paged = query.Get("detail_mode") == "paged"
	params.CommentsOffset, err = issueTaskInt(query, "comments_offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.CommentsLimit, err = issueTaskInt(query, "comments_limit", 50, 1, 200)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.HistoryOffset, err = issueTaskInt(query, "history_offset", 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.HistoryLimit, err = issueTaskInt(query, "history_limit", 50, 1, 200)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	afterID, err := issueTaskInt(query, "history_after_id", 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	params.HistoryAfterID = int64(afterID)
	result, err := s.issueStore.ReadTaskIssue(r.Context(), id, params)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	// Shadow the shared store's issue with the established role/origin projection.
	response := struct {
		gormdb.IssueTaskDetail
		Issue      issueReadItem `json:"issue"`
		SourceName string        `json:"source_project_display_name"`
		TargetName string        `json:"target_project_display_name"`
	}{IssueTaskDetail: result, Issue: issueReadProjection(result.Issue, result.Roles)}
	if result.Roles.SourceProjectID != nil {
		response.SourceName = s.getProjectDisplayName(r.Context(), *result.Roles.SourceProjectID)
	}
	if result.Roles.TargetProjectID != nil {
		response.TargetName = s.getProjectDisplayName(r.Context(), *result.Roles.TargetProjectID)
	}
	writeJSON(w, response)
}
