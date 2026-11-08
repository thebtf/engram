package worker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/issues"
	"github.com/thebtf/engram/internal/projectidentity"
)

type issueProjectOrigin struct {
	SourceProject string `json:"source_project"`
	TargetProject string `json:"target_project"`
}

type issueReadItem struct {
	gormdb.Issue
	gormdb.IssueProjectRoles
	ProjectOrigin issueProjectOrigin `json:"project_origin"`
}

type issueListReadItem struct {
	issueReadItem
	CommentCount int64 `json:"comment_count"`
}

func issueReadProjection(issue gormdb.Issue, roles gormdb.IssueProjectRoles) issueReadItem {
	return issueReadItem{Issue: issue, IssueProjectRoles: roles, ProjectOrigin: issueProjectOrigin{issue.SourceProject, issue.TargetProject}}
}

// issueMutationActor accepts only read-write SourceClient keycards or
// authenticated non-client admins. Project and agent request fields are
// attribution only.
func issueMutationActor(ctx context.Context) (keycardID string, isOperator bool, err error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return "", false, fmt.Errorf("%w: issue mutation forbidden: authenticated identity is required", gormdb.ErrIssueForbidden)
	}
	if id.IsAdmin() && id.Source != auth.SourceClient {
		return "", true, nil
	}
	if id.Source != auth.SourceClient || id.KeycardID == "" || id.Role != auth.RoleReadWrite {
		return "", false, fmt.Errorf("%w: issue mutation forbidden: authenticated read-write client keycard is required", gormdb.ErrIssueForbidden)
	}
	return id.KeycardID, false, nil
}

func (s *Service) authorizeIssueProgression(ctx context.Context) (bool, error) {
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return false, err
	}
	if err := s.issueStore.AuthorizeIssueProgressionMutation(ctx, keycardID, isOperator); err != nil {
		return false, err
	}
	return isOperator, nil
}

func (s *Service) authorizeIssueSourceMutation(ctx context.Context, id int64) (bool, error) {
	keycardID, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return false, err
	}
	if err := s.issueStore.AuthorizeIssueSourceMutation(ctx, id, keycardID, isOperator); err != nil {
		return false, err
	}
	return isOperator, nil
}

func requireIssueOperator(ctx context.Context) error {
	_, isOperator, err := issueMutationActor(ctx)
	if err != nil {
		return err
	}
	if !isOperator {
		return fmt.Errorf("%w: issue mutation forbidden: operator identity is required", gormdb.ErrIssueForbidden)
	}
	return nil
}

func writeIssueAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueNotFound):
		http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
	case errors.Is(err, gormdb.ErrIssueForbidden):
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusForbidden)
	default:
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
	}
}

func writeIssueStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueNotFound):
		http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
	case errors.Is(err, gormdb.ErrIssueForbidden):
		writeIssueAuthorizationError(w, err)
	case errors.Is(err, gormdb.ErrIssueInvalidInput), errors.Is(err, gormdb.ErrIssueInvalidTransition):
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusBadRequest)
	default:
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
	}
}

// handleListIssues godoc
// @Summary List issue-backed tasks
// @Description Returns {issues,total,limit,offset,next_offset,project_names}; summaries include project_origin and decimal-string revision, not body, metadata or history. Filters combine with AND and are not access grants. Each query parameter may occur at most once. Order is priority rank ascending, created_at descending, id descending. Offset paging is live, not a snapshot; retain filters, deduplicate IDs and restart after material refresh. next_offset is the first unreturned row or null. Project names cover returned authorized project roles only.
// @Tags Issues
// @Produce json
// @Security ApiKeyAuth
// @Param project query string false "Canonical target-project UUID filter, not caller identity"
// @Param source_project query string false "Independent canonical source-project UUID filter"
// @Param source_agent query string false "Exact source-agent attribution label, not a principal"
// @Param source_session query string false "Exact creator session attribution, not authentication or authority"
// @Param target_session query string false "Exact recipient session attribution, not authentication or authority"
// @Param participant_session query string false "Exact recorded creator, comment, lifecycle event or material revision session attribution"
// @Param status query string false "Comma-separated open, acknowledged, resolved, reopened, closed, rejected; OR within this filter"
// @Param type query string false "Issue type, trimmed and lowercased" Enums(bug,feature,improvement,task)
// @Param q query string false "Case-insensitive literal title OR body substring; not metadata, comments or history"
// @Param owner_label query string false "Exact non-authoritative owner label"
// @Param recipient_label query string false "Exact non-authoritative recipient label"
// @Param resolved_since query int64 false "Inclusive resolved_at lower bound in signed epoch milliseconds"
// @Param limit query int false "Maximum summaries" default(50) minimum(1) maximum(200)
// @Param offset query int false "Nonnegative in-range offset" default(0) minimum(0)
// @Success 200 {object} object{issues=[]gormdb.IssueTaskSummary{project_origin=issueProjectOrigin},total=integer,limit=integer,offset=integer,next_offset=integer,project_names=map[string]string} "Authorized summary page; next_offset may be null"
// @Failure 400 {object} map[string]issues.Error "INVALID_ARGUMENT; invalid or repeated query parameter or bound"
// @Failure 401 "Authentication required; middleware may return plain text, issue-owner UNAUTHENTICATED uses a JSON error envelope"
// @Failure 403 "Authentication middleware may return plain text; filters do not grant access"
// @Failure 404 {object} map[string]issues.Error "ISSUE_NOT_FOUND; canonical filter project unavailable"
// @Failure 500 "Authentication infrastructure failure"
// @Failure 503 {object} map[string]issues.Error "UNAVAILABLE; dependency failure, not an empty page"
// @Router /api/issues [get]
func (s *Service) handleListIssues(w http.ResponseWriter, r *http.Request) {
	s.listIssueTasks(w, r)
}

// handleGetIssue godoc
// @Summary Read issue detail and independent comment and history pages
// @Description Returns authorized issue, comments and material revision history, their counts, effective offsets and limits, nullable next offsets, complete and oversized flags, and source/target display names. Issue detail includes decimal-string revision, object metadata, nullable labels, actor and project roles; creator credentials are excluded. Comments are ordered by created_at/id and material history by revision. History contains field names and admitted changes; absent historical values are not invented.
// @Description detail_mode=paged requests independent comments and material-history pages, each default 50 and maximum 200, targeting 4 MiB serialized bytes. An intact oversized singleton is flagged. A next offset identifies the first unreturned record; complete means no later records, not that earlier pages were loaded. Pages are live, not a frozen snapshot. Without detail_mode, complete comments exceeding 200 records or 4 MiB fail DETAIL_PAGING_REQUIRED without partial success. Lifecycle history_events are separate from material revisions: history_after_id is an exclusive ID cursor, event count is bounded by history_limit and capped at 100, and history_has_more, history_next_after_id and history_events_oversized describe continuation. No project filter grants access. Swagger 2 cannot express all nullable and conditional fields.
// @Tags Issues
// @Produce json
// @Security ApiKeyAuth
// @Param id path int64 true "Positive issue ID" minimum(1)
// @Param project query string false "Optional canonical source/target-role read narrowing, not an access grant"
// @Param detail_mode query string false "Explicit paging; omission requests legacy complete comments" Enums(paged)
// @Param comments_offset query int false "Nonnegative comment offset" default(0) minimum(0)
// @Param comments_limit query int false "Comment page limit" default(50) minimum(1) maximum(200)
// @Param history_offset query int false "Independent material history offset" default(0) minimum(0)
// @Param history_limit query int false "Material history page limit" default(50) minimum(1) maximum(200)
// @Param history_after_id query int false "Exclusive lifecycle event ID cursor, separate from history_offset" default(0) minimum(0)
// @Success 200 {object} object{issue=object,comments=[]object,history=[]object,history_events=[]object,history_has_more=boolean,history_next_after_id=integer,history_events_oversized=boolean,comment_count=integer,history_count=integer,comments_offset=integer,comments_limit=integer,history_offset=integer,history_limit=integer,comments_next_offset=integer,history_next_offset=integer,comments_complete=boolean,history_complete=boolean,comments_oversized=boolean,history_oversized=boolean,source_project_display_name=string,target_project_display_name=string} "Authorized intact detail; continuation offsets and cursor may be null"
// @Failure 400 {object} map[string]issues.Error "INVALID_ARGUMENT; invalid ID, query, mode or bounds"
// @Failure 401 "Authentication required; middleware may return plain text, issue-owner UNAUTHENTICATED uses a JSON error envelope"
// @Failure 403 "Authentication middleware may return plain text"
// @Failure 404 {object} map[string]issues.Error "ISSUE_NOT_FOUND; missing or unreadable issue or project"
// @Failure 409 {object} map[string]issues.Error "DETAIL_PAGING_REQUIRED; use detail_mode=paged with separate continuations"
// @Failure 500 "Authentication infrastructure failure"
// @Failure 503 {object} map[string]issues.Error "UNAVAILABLE; read failure, not empty history"
// @Router /api/issues/{id} [get]
func (s *Service) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	s.getIssueTask(w, r)
}

func urlParseIssueDetail(r *http.Request) (string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", err
	}
	if !query.Has("project") {
		return "", nil
	}
	if len(query["project"]) != 1 || !projectidentity.ValidProjectID(query.Get("project")) {
		return "", gormdb.ErrIssueForbidden
	}
	return query.Get("project"), nil
}

// handleCreateIssue godoc
// @Summary Create or reconcile an issue-backed task
// @Description JSON fields include required nonempty title and active canonical source_project; optional target_project defaults to source only when omitted, and optional nonempty target_session is recipient provenance, not authority. body defaults to empty, type to task, priority to medium, labels to [], metadata to {}, and owner_label/recipient_label to null. Labels and attribution are not credentials or access grants. Metadata is an object with compact size at most 1 MiB and at most 256 nested containers. Raw duplicate members, invalid Unicode and unpreservable numbers are rejected before mutation. Applicable recognized-secret protection rejects atomically without rewriting; detection is not comprehensive. The request ceiling is 10 MiB.
// @Description An optional nonempty UTF-8 request_key of at most 512 bytes without NUL is scoped by authenticated actor and canonical source. Retain the original actor, source, key and logical payload after an unknown outcome. Same-key same-payload retry returns the original ID and current authorized revision; a changed payload conflicts and a deleted receipt cannot resurrect an issue. expected_actor is an optional keyed-create precondition obtained from auth-me, never a credential; mismatch or non-comparable identity returns ACTOR_CHANGED before receipt lookup. Unkeyed create is single-shot and has no replay guarantee. The result is {id,revision,replayed,changed,comment_id,message}; receipt payloads, digests and credentials are not returned. Swagger 2 cannot faithfully express all object, null and conditional rules.
// @Tags Issues
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body object true "Create object as described; metadata is an object and expected_actor is a precondition only"
// @Success 201 {object} issues.Result "New issue committed; replayed=false"
// @Success 200 {object} issues.Result "Original authorized intent replayed; replayed=true"
// @Failure 400 {object} map[string]issues.Error "INVALID_ARGUMENT or PRIVACY_REJECTED; no write"
// @Failure 401 "Authentication required; middleware may return plain text, issue-owner UNAUTHENTICATED uses a JSON error envelope"
// @Failure 403 "FORBIDDEN in the issue JSON envelope or plain-text middleware refusal"
// @Failure 404 {object} map[string]issues.Error "ISSUE_NOT_FOUND; canonical scope or replay target unavailable"
// @Failure 409 {object} map[string]issues.Error "ACTOR_CHANGED or IDEMPOTENCY_CONFLICT; retain and reconcile the original intent"
// @Failure 410 {object} map[string]issues.Error "ISSUE_GONE; a retained receipt cannot create a replacement"
// @Failure 413 "PAYLOAD_TOO_LARGE in the issue JSON envelope; outer request-size middleware may return plain text"
// @Failure 500 "Authentication infrastructure failure"
// @Failure 503 {object} map[string]issues.Error "UNAVAILABLE; reconcile keyed intent before retry, never blindly retry unkeyed create"
// @Router /api/issues [post]
func (s *Service) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	raw, scope, err := readIssueTaskWrite(w, r)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	result, err := issues.New(s.issueStore, s.redactionRules).Create(r.Context(), scope, raw)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !result.Replayed {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// handleUpdateIssue godoc
// @Summary Atomically edit an issue or append a comment
// @Description Presence-sensitive JSON fields include expected_revision, title, body, priority, type, status, labels, metadata, owner_label, recipient_label, target_session, comment and comment_metadata. A revision is a canonical decimal string from 1 through 9223372036854775807. Omitted fields stay unchanged; empty body and labels are permitted, empty metadata replaces the object, and null clears an owner label, recipient label or target session. Empty title or an empty non-null owner/recipient label is invalid. comment must be nonempty; comment_metadata requires comment and defaults to {}. Source/target/creator attribution is not editable. Optional source_project/source_agent/session fields are provenance, not authentication or authority; omitted source is admitted from the locked issue without inventing caller attribution.
// @Description Material fields and status require expected_revision; comment-only append may omit it, but a supplied revision is checked. A material change, its revision/history and optional comment commit atomically; a stale revision commits none of the request. Retain the draft and base, reread and explicitly reconcile. A same-value material request at the current revision is a no-op unless a comment is added. Comments do not advance revision and are not deduplicated. Protected material fields require creator-keycard or operator authority. Authorized read-write participants may resolve, close or reopen; acknowledged, rejected and open transitions remain operator-only. Rejection requires a nonempty comment. Mixed requests apply the strictest required authority.
// @Description Request ceiling is 10 MiB; admitted metadata objects are limited to 1 MiB compact JSON and 256 nested containers. Duplicate raw members, invalid Unicode and unpreservable numbers are rejected. Applicable recognized-secret protection rejects atomically without rewriting and is not comprehensive detection. The result is {id,revision,replayed,changed,comment_id,message}, not a data wrapper; comment_id may be null. Issue-owner errors use {error:{code,message,current_revision?}}; outer middleware may return plain text. Swagger 2 cannot encode all presence, null and conditional rules.
// @Tags Issues
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int64 true "Positive issue ID" minimum(1)
// @Param project query string false "Optional canonical source/target-role read narrowing, not actor identity"
// @Param body body object true "Presence-sensitive mutation; material edits require expected_revision"
// @Success 200 {object} issues.Result "Atomic update, comment append or current-revision no-op"
// @Failure 400 {object} map[string]issues.Error "INVALID_ARGUMENT or PRIVACY_REJECTED; no write"
// @Failure 401 "Authentication required; middleware may return plain text, issue-owner UNAUTHENTICATED uses a JSON error envelope"
// @Failure 403 "FORBIDDEN in the issue JSON envelope or plain-text middleware refusal"
// @Failure 404 {object} map[string]issues.Error "ISSUE_NOT_FOUND; missing or unreadable issue or canonical scope"
// @Failure 409 {object} map[string]issues.Error "REVISION_CONFLICT or INVALID_TRANSITION; no compound comment committed"
// @Failure 413 "PAYLOAD_TOO_LARGE in the issue JSON envelope; outer request-size middleware may return plain text"
// @Failure 428 {object} map[string]issues.Error "REVISION_REQUIRED; read current revision before material mutation"
// @Failure 500 "Authentication infrastructure failure"
// @Failure 503 {object} map[string]issues.Error "UNAVAILABLE; reconcile outcome before retrying"
// @Router /api/issues/{id} [patch]
func (s *Service) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
		return
	}
	raw, scope, err := readIssueTaskWrite(w, r)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	scope.ReadProject, err = urlParseIssueDetail(r)
	if err != nil {
		writeIssueTaskError(w, issues.Failure("INVALID_ARGUMENT"))
		return
	}
	result, err := issues.New(s.issueStore, s.redactionRules).Mutate(r.Context(), scope, id, raw)
	if err != nil {
		writeIssueTaskError(w, err)
		return
	}
	writeJSON(w, result)
}

type issueAcknowledgeRequest struct {
	IDs           []int64 `json:"ids"`
	SourceProject string  `json:"source_project"`
	SourceAgent   string  `json:"source_agent"`
}

func validateIssueMutationSource(ctx context.Context, sourceProject, sourceAgent string) error {
	identity, _ := auth.IdentityFrom(ctx)
	human := identity.Source == auth.SourceSession || identity.PrincipalKind == auth.PrincipalKindHuman || sourceAgent == "human"
	if human && (sourceAgent != "human" || !projectidentity.ValidProjectID(sourceProject)) {
		return fmt.Errorf("%w: human issue mutations require source_project as an exact canonical UUID and source_agent=human", gormdb.ErrIssueInvalidInput)
	}
	return nil
}

func issueMutationAudit(ctx context.Context, action, sourceProject, sourceAgent string, ids []int64) *gormdb.AuditLogEntry {
	if sourceProject == "" && sourceAgent == "" {
		return nil
	}
	actor, sessionID := configAuditIdentity(ctx)
	if identity, ok := auth.IdentityFrom(ctx); ok {
		if subject, found := identity.SessionBrowserSubject(); found {
			actor = string(identity.Source) + ":human:" + subject.Principal
		}
	}
	return &gormdb.AuditLogEntry{
		Action:          "issue." + action,
		Actor:           actor,
		SourceSessionID: sessionID,
		AfterState: configAuditRaw(map[string]any{
			"source_project": sourceProject,
			"source_agent":   sourceAgent,
			"issue_ids":      ids,
		}),
	}
}

func issueMutationAttribution(ctx context.Context, sourceProject, sourceAgent string) gormdb.IssueChangeAttribution {
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	_, session := configAuditIdentity(ctx)
	return gormdb.IssueChangeAttribution{SourceProject: optional(sourceProject), SourceAgent: optional(sourceAgent), Session: optional(session)}
}

// handleAcknowledgeIssues handles POST /api/issues/acknowledge.
func (s *Service) handleAcknowledgeIssues(w http.ResponseWriter, r *http.Request) {
	if err := requireIssueOperator(r.Context()); err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}
	var req issueAcknowledgeRequest
	if _, err := operatorCodeReadJSON(r, &req); err != nil {
		http.Error(w, `{"error": "invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if err := validateIssueMutationSource(r.Context(), req.SourceProject, req.SourceAgent); err != nil {
		writeIssueStoreError(w, err)
		return
	}

	acknowledged, err := s.issueStore.AcknowledgeIssuesAtomically(r.Context(), req.IDs, issueMutationAudit(r.Context(), "acknowledge", req.SourceProject, req.SourceAgent, req.IDs), issueMutationAttribution(r.Context(), req.SourceProject, req.SourceAgent))
	if err != nil {
		writeIssueStoreError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"acknowledged": acknowledged,
	})
}

// handleTrackedProjects handles GET /api/issues/tracked-projects.
// Returns the set of projects that use engram's issue system, so agents can
// tell "is this project in engram?" — if not, they should use GitHub/Linear/etc.
func (s *Service) handleTrackedProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.issueStore.GetTrackedProjects(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"projects": projects,
		"count":    len(projects),
	})
}

// handleDeleteIssue handles DELETE /api/issues/{id}. Hard delete — intended for dashboard operators.
func (s *Service) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"error": "invalid issue id"}`, http.StatusBadRequest)
		return
	}
	if err := requireIssueOperator(r.Context()); err != nil {
		writeIssueAuthorizationError(w, err)
		return
	}
	query := r.URL.Query()
	if len(query["source_project"]) > 1 || len(query["source_agent"]) > 1 {
		writeDocumentError(w, http.StatusBadRequest, "issue attribution must be supplied exactly once")
		return
	}
	sourceProject, sourceAgent := query.Get("source_project"), query.Get("source_agent")
	if err := validateIssueMutationSource(r.Context(), sourceProject, sourceAgent); err != nil {
		writeIssueStoreError(w, err)
		return
	}
	if err := s.issueStore.DeleteIssue(r.Context(), id, issueMutationAudit(r.Context(), "delete", sourceProject, sourceAgent, []int64{id})); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, `{"error": "issue not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	log.Info().Int64("issue_id", id).Str("source_project", sourceProject).Str("source_agent", sourceAgent).Msg("Issue deleted by operator")
	w.WriteHeader(http.StatusNoContent)
}

const (
	issueSelectionDomain      = "issues"
	issueSelectionCursorV1    = 1
	issueSelectionFilterLimit = 64
)

type issueSelectionFilter struct {
	Project       string   `json:"project,omitempty"`
	SourceProject string   `json:"source_project,omitempty"`
	SourceAgent   string   `json:"source_agent,omitempty"`
	Statuses      []string `json:"statuses,omitempty"`
	Type          string   `json:"type,omitempty"`
}

type issueSelectionCursor struct {
	Fingerprint string               `json:"f"`
	Revision    string               `json:"r"`
	Filter      issueSelectionFilter `json:"q"`
	Offset      int                  `json:"o"`
	Limit       int                  `json:"l"`
	Version     int                  `json:"v"`
}

type issueSelectionSnapshotRequest struct {
	Selection     issueSelectionRequest `json:"selection"`
	SourceProject string                `json:"source_project"`
	SourceAgent   string                `json:"source_agent"`
}

type issueSelectionRequest struct {
	Kind        gormdb.CollectionSelectionKind     `json:"kind"`
	Targets     []gormdb.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor      string                             `json:"cursor,omitempty"`
	Filter      *issueSelectionFilter              `json:"filter,omitempty"`
	ExcludedIDs []string                           `json:"excluded_ids,omitempty"`
}

type issueSelectionCurrentRequest struct {
	SourceProject string `json:"source_project"`
	SourceAgent   string `json:"source_agent"`
}

type issueSelectionPageRequest struct {
	Filter        *issueSelectionFilter `json:"filter"`
	Cursor        string                `json:"cursor,omitempty"`
	Limit         int                   `json:"limit"`
	SourceProject string                `json:"source_project"`
	SourceAgent   string                `json:"source_agent"`
}

type issueSelectionOperationRequest struct {
	RequestID     string                    `json:"request_id"`
	Action        string                    `json:"action"`
	Selection     issueSelectionActionScope `json:"selection"`
	Status        *string                   `json:"status,omitempty"`
	Priority      *string                   `json:"priority,omitempty"`
	Labels        *[]string                 `json:"labels,omitempty"`
	SourceProject string                    `json:"source_project"`
	SourceAgent   string                    `json:"source_agent"`
}

type issueSelectionActionScope struct {
	Kind    gormdb.CollectionSelectionKind `json:"kind"`
	Version int64                          `json:"selection_version"`
	Token   string                         `json:"selection_token,omitempty"`
}

type issueSelectionSnapshotResponse struct {
	Selection issueSelectionSnapshot `json:"selection"`
}

type issueSelectionSnapshot struct {
	Domain                 string                             `json:"domain"`
	Kind                   gormdb.CollectionSelectionKind     `json:"kind"`
	Version                int64                              `json:"selection_version"`
	Targets                []gormdb.CollectionSelectionTarget `json:"targets,omitempty"`
	Cursor                 string                             `json:"cursor,omitempty"`
	FilterFingerprint      string                             `json:"filter_fingerprint,omitempty"`
	TargetCount            int                                `json:"target_count,omitempty"`
	ExcludedIDs            []string                           `json:"excluded_ids,omitempty"`
	Token                  string                             `json:"selection_token,omitempty"`
	ExpiresAt              string                             `json:"expires_at,omitempty"`
	ReconfirmationRequired bool                               `json:"reconfirmation_required"`
	ReconfirmationReason   string                             `json:"reconfirmation_reason,omitempty"`
}

type issueSelectionPageResponse struct {
	FilterFingerprint string                             `json:"filter_fingerprint"`
	Cursor            string                             `json:"cursor"`
	Targets           []gormdb.CollectionSelectionTarget `json:"targets"`
	NextCursor        string                             `json:"next_cursor"`
	Total             int64                              `json:"total"`
}

type issueSelectionOperationResponse struct {
	RequestID      string                          `json:"request_id"`
	OperationState string                          `json:"operation_state"`
	ItemResults    []issueSelectionOperationItem   `json:"item_results"`
	Readback       issueSelectionOperationReadback `json:"readback"`
}

type issueSelectionOperationItem struct {
	TargetID        int64  `json:"target_id"`
	Outcome         string `json:"outcome"`
	ObservedVersion uint64 `json:"observed_version,omitempty"`
}

type issueSelectionOperationReadback struct {
	Authoritative bool                         `json:"authoritative"`
	Kind          string                       `json:"kind"`
	CurrentState  []issueSelectionCurrentState `json:"current_state"`
}

type issueSelectionCurrentState struct {
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Labels    []string `json:"labels"`
	UpdatedAt string   `json:"updated_at"`
}

// HandleIssueSelectionSnapshot saves one browser-scoped Issues selection.
// T031 owns route registration; this handler owns only the Issues boundary.
func (s *Service) HandleIssueSelectionSnapshot(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionSnapshotRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}

	selection, err := s.issueSelectionSnapshot(r.Context(), request.Selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	if selection.Kind == gormdb.CollectionSelectionFrozenFilter {
		filter, filterErr := normalizeIssueSelectionFilter(request.Selection.Filter)
		if filterErr != nil {
			writeIssueSelectionError(w, filterErr)
			return
		}
		rows, rowsErr := s.issueSelectionRows(r.Context(), filter)
		if rowsErr != nil {
			writeIssueSelectionError(w, rowsErr)
			return
		}
		selection.FilterFingerprint = issueSelectionActorFilterFingerprint(filter.collectionFilter().Fingerprint, scope.ContextFingerprint)
		selection.Targets = issueSelectionTargets(rows)
		selection.ExpiresAt = time.Now().UTC().Add(time.Hour)
	} else if selection.Kind == gormdb.CollectionSelectionPage {
		page, pageErr := s.issueSelectionPage(r.Context(), nil, selection.Cursor, 0)
		if pageErr != nil {
			writeIssueSelectionError(w, pageErr)
			return
		}
		selection.Targets = page.Targets
	} else if selection.Kind == gormdb.CollectionSelectionExplicit {
		selection.Targets, err = s.issueSelectionExplicitTargets(r.Context(), selection.Targets)
		if err != nil {
			writeIssueSelectionError(w, err)
			return
		}
	}
	if err := gormdb.ValidateCollectionSelectionInput(selection); err != nil {
		writeIssueSelectionError(w, err)
		return
	}

	saved, err := selections.Save(r.Context(), scope, selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionSnapshotResponse{Selection: newIssueSelectionSnapshot(saved)})
}

// HandleIssueSelectionCurrent returns the saved selection only for its exact browser scope.
func (s *Service) HandleIssueSelectionCurrent(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionCurrentRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	selection, err := selections.Current(r.Context(), scope)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionSnapshotResponse{Selection: newIssueSelectionSnapshot(selection)})
}

// HandleIssueSelectionPage returns one bounded, cursor-validated Issues page.
func (s *Service) HandleIssueSelectionPage(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionPageRequest
	_, _, _, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	page, err := s.issueSelectionPage(r.Context(), request.Filter, request.Cursor, request.Limit)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	writeJSON(w, issueSelectionPageResponse{
		FilterFingerprint: page.filter.Fingerprint,
		Cursor:            page.Cursor,
		Targets:           page.Targets,
		NextCursor:        page.NextCursor,
		Total:             page.Total,
	})
}

// HandleIssueSelectionOperation resolves the current saved selection and delegates
// all locking, permission rechecks, revision checks, and mutation to IssueStore.
func (s *Service) HandleIssueSelectionOperation(w http.ResponseWriter, r *http.Request) {
	var request issueSelectionOperationRequest
	_, scope, selections, ok := s.issueSelectionRequestScope(w, r, &request)
	if !ok {
		return
	}
	if !operatorCodeText(request.RequestID) || r.Header.Get("X-Engram-Request-ID") != request.RequestID {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return
	}

	action, err := issueSelectionAction(request)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	if action.Kind == gormdb.IssueSelectionLabels && action.Labels != nil {
		if err := issues.ValidateLabelsPrivacy(*action.Labels, s.redactionRules); err != nil {
			operatorCodeWriteBodyless(w, http.StatusBadRequest)
			return
		}
	}
	selection, err := issueOperationSelection(r.Context(), selections, scope, request.Selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	targets, err := issueSelectionOperationTargets(selection)
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	keycardID, isOperator, err := issueMutationActor(r.Context())
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}
	ids := make([]int64, len(targets))
	for index, target := range targets {
		ids[index] = target.ID
	}
	result, err := s.issueStore.ApplyIssueSelectionOperation(r.Context(), gormdb.IssueSelectionOperation{
		Action:      action,
		Actor:       gormdb.IssueSelectionActor{KeycardID: keycardID, IsOperator: isOperator},
		Targets:     targets,
		Audit:       issueMutationAudit(r.Context(), request.Action, request.SourceProject, request.SourceAgent, ids),
		Attribution: issueMutationAttribution(r.Context(), request.SourceProject, request.SourceAgent),
	})
	if err != nil {
		writeIssueSelectionError(w, err)
		return
	}

	items := make([]issueSelectionOperationItem, 0, len(result.Items))
	current := make([]issueSelectionCurrentState, 0, len(result.Items))
	for _, item := range result.Items {
		response := issueSelectionOperationItem{TargetID: item.TargetID, Outcome: string(item.Outcome)}
		if item.Readback != nil {
			response.ObservedVersion = issueSelectionRevision(item.Readback.UpdatedAt)
			current = append(current, issueSelectionCurrentState{
				Status:    item.Readback.Status,
				Priority:  item.Readback.Priority,
				Labels:    append([]string(nil), item.Readback.Labels...),
				UpdatedAt: item.Readback.UpdatedAt.UTC().Format(time.RFC3339Nano),
			})
		}
		items = append(items, response)
	}
	writeJSON(w, issueSelectionOperationResponse{
		RequestID:      request.RequestID,
		OperationState: "completed",
		ItemResults:    items,
		Readback: issueSelectionOperationReadback{
			Authoritative: true,
			Kind:          "current",
			CurrentState:  current,
		},
	})
}

type issueSelectionPage struct {
	filter     gormdb.CollectionFilter
	Cursor     string
	Targets    []gormdb.CollectionSelectionTarget
	NextCursor string
	Total      int64
}

const issueSelectionDigestPrefix = "sha256:"

func (s *Service) issueSelectionRequestScope(w http.ResponseWriter, r *http.Request, target any) (auth.Identity, gormdb.CollectionSelectionScope, *gormdb.CollectionSelectionStore, bool) {
	if s == nil || s.store == nil || s.issueStore == nil || r == nil {
		operatorCodeWriteBodyless(w, http.StatusServiceUnavailable)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	identity, found := auth.IdentityFrom(r.Context())
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	subject, found := identity.SessionBrowserSubject()
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	sessionID, found := operatorCodeSessionID(r)
	if !found {
		operatorCodeWriteBodyless(w, http.StatusForbidden)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	if !operatorCodeText(r.Header.Get("X-Engram-Request-ID")) {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	if _, err := operatorCodeReadJSON(r, target); err != nil {
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	sourceProject, sourceAgent, err := issueSelectionSource(r.Context(), target)
	if err != nil {
		writeIssueSelectionError(w, err)
		return auth.Identity{}, gormdb.CollectionSelectionScope{}, nil, false
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("issues-collection-scope/v2\x00%d\x00%s\x00%s\x00%s\x00%s", subject.UserID, sessionID, issueSelectionDomain, sourceProject, sourceAgent)))
	return identity, gormdb.CollectionSelectionScope{
		SubjectUserID:      subject.UserID,
		SessionID:          sessionID,
		Domain:             issueSelectionDomain,
		ContextFingerprint: issueSelectionDigestPrefix + hex.EncodeToString(digest[:]),
		AuthorizationEpoch: 1,
		CollectionVersion:  1,
	}, gormdb.NewCollectionSelectionStore(s.store.GetDB()), true
}

func issueSelectionActorFilterFingerprint(rowFingerprint, contextFingerprint string) string {
	digest := sha256.Sum256([]byte("issues-actor-filter/v1\x00" + rowFingerprint + "\x00" + contextFingerprint))
	return issueSelectionDigestPrefix + hex.EncodeToString(digest[:])
}

func issueSelectionSource(ctx context.Context, target any) (string, string, error) {
	var sourceProject, sourceAgent, cursor string
	var filter *issueSelectionFilter
	switch request := target.(type) {
	case *issueSelectionSnapshotRequest:
		sourceProject, sourceAgent = request.SourceProject, request.SourceAgent
		filter, cursor = request.Selection.Filter, request.Selection.Cursor
	case *issueSelectionCurrentRequest:
		sourceProject, sourceAgent = request.SourceProject, request.SourceAgent
	case *issueSelectionPageRequest:
		sourceProject, sourceAgent = request.SourceProject, request.SourceAgent
		filter, cursor = request.Filter, request.Cursor
	case *issueSelectionOperationRequest:
		sourceProject, sourceAgent = request.SourceProject, request.SourceAgent
	default:
		return "", "", gormdb.ErrCollectionSelectionInvalid
	}
	if err := validateIssueMutationSource(ctx, sourceProject, sourceAgent); err != nil {
		return "", "", err
	}
	if cursor != "" {
		resolved, _, err := issueSelectionPageFilter(filter, cursor)
		if err != nil {
			return "", "", err
		}
		filter = &resolved
	}
	if filter != nil {
		normalized, err := normalizeIssueSelectionFilter(filter)
		if err != nil {
			return "", "", err
		}
		selected := normalized.Project
		if selected == "" {
			selected = normalized.SourceProject
		}
		if sourceAgent == "human" && selected != "" && sourceProject != selected {
			return "", "", gormdb.ErrCollectionSelectionInvalid
		}
	}
	return sourceProject, sourceAgent, nil
}

func (s *Service) issueSelectionSnapshot(ctx context.Context, request issueSelectionRequest) (gormdb.CollectionSelection, error) {
	selection := gormdb.CollectionSelection{
		Kind:        request.Kind,
		Targets:     append([]gormdb.CollectionSelectionTarget(nil), request.Targets...),
		Cursor:      request.Cursor,
		ExcludedIDs: append([]string(nil), request.ExcludedIDs...),
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionNone:
		if len(selection.Targets) != 0 || selection.Cursor != "" || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionExplicit:
		if selection.Cursor != "" || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionPage:
		if selection.Cursor == "" || len(selection.Targets) != 0 || request.Filter != nil || len(selection.ExcludedIDs) != 0 {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	case gormdb.CollectionSelectionFrozenFilter:
		if request.Filter == nil || len(selection.Targets) != 0 || selection.Cursor != "" {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
	default:
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	return selection, nil
}

func (s *Service) issueSelectionExplicitTargets(ctx context.Context, targets []gormdb.CollectionSelectionTarget) ([]gormdb.CollectionSelectionTarget, error) {
	if len(targets) == 0 || len(targets) > gormdb.CollectionSelectionMaxTargets {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	resolved := make([]gormdb.CollectionSelectionTarget, 0, len(targets))
	seen := make(map[int64]struct{}, len(targets))
	for _, target := range targets {
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		if _, found := seen[id]; found {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		issue, _, err := s.issueStore.GetIssue(ctx, id)
		if err != nil {
			return nil, gormdb.ErrCollectionSelectionDenied
		}
		seen[id] = struct{}{}
		resolved = append(resolved, gormdb.CollectionSelectionTarget{ID: target.ID, ExpectedVersion: issueSelectionRevision(issue.UpdatedAt)})
	}
	return resolved, nil
}

func (s *Service) issueSelectionRows(ctx context.Context, filter issueSelectionFilter) ([]gormdb.IssueWithCount, error) {
	params := gormdb.IssueListParams{
		TargetProject: filter.Project,
		SourceProject: filter.SourceProject,
		SourceAgent:   filter.SourceAgent,
		Statuses:      append([]string(nil), filter.Statuses...),
		Type:          filter.Type,
		Limit:         gormdb.CollectionSelectionMaxTargets + 1,
	}
	rows, total, err := s.issueStore.ListIssuesEx(ctx, params)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, gormdb.ErrCollectionSelectionDenied
	}
	if total > gormdb.CollectionSelectionMaxTargets || int64(len(rows)) != total {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return rows, nil
}

func (s *Service) issueSelectionPage(ctx context.Context, requestedFilter *issueSelectionFilter, cursor string, requestedLimit int) (issueSelectionPage, error) {
	filter, state, err := issueSelectionPageFilter(requestedFilter, cursor)
	if err != nil {
		return issueSelectionPage{}, err
	}
	limit := requestedLimit
	if limit == 0 && cursor != "" {
		limit = state.Limit
	}
	if limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return issueSelectionPage{}, gormdb.ErrCollectionSelectionInvalid
	}
	return s.issueSelectionPageResult(ctx, filter, state, cursor, limit)
}

func issueSelectionPageFilter(requestedFilter *issueSelectionFilter, cursor string) (issueSelectionFilter, issueSelectionCursor, error) {
	if cursor == "" {
		filter, err := normalizeIssueSelectionFilter(requestedFilter)
		return filter, issueSelectionCursor{}, err
	}
	state, err := decodeIssueSelectionCursor(cursor)
	if err != nil {
		return issueSelectionFilter{}, issueSelectionCursor{}, err
	}
	if requestedFilter == nil {
		return state.Filter, state, nil
	}
	normalized, normalizeErr := normalizeIssueSelectionFilter(requestedFilter)
	if normalizeErr != nil || !sameIssueSelectionFilter(normalized, state.Filter) {
		return issueSelectionFilter{}, issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	return state.Filter, state, nil
}

func (s *Service) issueSelectionPageResult(ctx context.Context, filter issueSelectionFilter, state issueSelectionCursor, cursor string, limit int) (issueSelectionPage, error) {
	rows, err := s.issueSelectionRows(ctx, filter)
	if err != nil {
		return issueSelectionPage{}, err
	}
	collectionFilter := filter.collectionFilter()
	revision := issueSelectionRevisionDigest(collectionFilter, rows)
	offset := 0
	if cursor != "" {
		if state.Fingerprint != collectionFilter.Fingerprint || state.Revision != revision || state.Limit != limit || state.Offset < 0 || state.Offset >= len(rows) {
			return issueSelectionPage{}, gormdb.ErrCollectionSelectionReconfirmationRequired
		}
		offset = state.Offset
	} else {
		cursor, err = encodeIssueSelectionCursor(collectionFilter, revision, filter, 0, limit)
		if err != nil {
			return issueSelectionPage{}, err
		}
	}
	end := min(offset+limit, len(rows))
	var nextCursor string
	if end < len(rows) {
		nextCursor, err = encodeIssueSelectionCursor(collectionFilter, revision, filter, end, limit)
		if err != nil {
			return issueSelectionPage{}, err
		}
	}
	return issueSelectionPage{
		filter:     collectionFilter,
		Cursor:     cursor,
		Targets:    issueSelectionTargets(rows[offset:end]),
		NextCursor: nextCursor,
		Total:      int64(len(rows)),
	}, nil
}

func normalizeIssueSelectionFilter(request *issueSelectionFilter) (issueSelectionFilter, error) {
	if request == nil {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter := issueSelectionFilter{
		Project:       strings.TrimSpace(request.Project),
		SourceProject: strings.TrimSpace(request.SourceProject),
		SourceAgent:   strings.TrimSpace(request.SourceAgent),
		Type:          strings.ToLower(strings.TrimSpace(request.Type)),
	}
	if filter.Project != request.Project || filter.SourceProject != request.SourceProject || filter.SourceAgent != request.SourceAgent || filter.Type != request.Type ||
		(len(filter.Project) > issueSelectionFilterLimit || len(filter.SourceProject) > issueSelectionFilterLimit || len(filter.SourceAgent) > issueSelectionFilterLimit) {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	for _, project := range []string{filter.Project, filter.SourceProject} {
		if project != "" && !projectidentity.ValidProjectID(project) {
			return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
		}
	}
	if filter.Type != "" && filter.Type != "bug" && filter.Type != "feature" && filter.Type != "improvement" && filter.Type != "task" {
		return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
	}
	statuses := make([]string, 0, len(request.Statuses))
	seen := make(map[string]struct{}, len(request.Statuses))
	for _, status := range request.Statuses {
		normalized := strings.ToLower(strings.TrimSpace(status))
		if normalized != status || !validIssueSelectionStatus(normalized) {
			return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
		}
		if _, found := seen[normalized]; found {
			return issueSelectionFilter{}, gormdb.ErrCollectionSelectionInvalid
		}
		seen[normalized] = struct{}{}
		statuses = append(statuses, normalized)
	}
	sort.Strings(statuses)
	filter.Statuses = statuses
	return filter, nil
}

func sameIssueSelectionFilter(left, right issueSelectionFilter) bool {
	if left.Project != right.Project || left.SourceProject != right.SourceProject || left.SourceAgent != right.SourceAgent || left.Type != right.Type || len(left.Statuses) != len(right.Statuses) {
		return false
	}
	for index := range left.Statuses {
		if left.Statuses[index] != right.Statuses[index] {
			return false
		}
	}
	return true
}

func validIssueSelectionStatus(value string) bool {
	switch value {
	case "open", "acknowledged", "reopened", "resolved", "closed", "rejected":
		return true
	default:
		return false
	}
}

func (filter issueSelectionFilter) collectionFilter() gormdb.CollectionFilter {
	digest := sha256.Sum256([]byte("issues-collection-filter/v1\x00priority:asc\x00created_at:desc\x00" + filter.Project + "\x00" + filter.SourceProject + "\x00" + strings.Join(filter.Statuses, ",") + "\x00" + filter.Type + "\x00" + filter.SourceAgent))
	return gormdb.CollectionFilter{Fingerprint: issueSelectionDigestPrefix + hex.EncodeToString(digest[:]), Value: "issues"}
}

func issueSelectionRevisionDigest(filter gormdb.CollectionFilter, rows []gormdb.IssueWithCount) string {
	hash := sha256.New()
	hash.Write([]byte("issues-collection-revision/v1\x00" + filter.Fingerprint + "\x00"))
	for _, row := range rows {
		hash.Write([]byte(strconv.FormatInt(row.ID, 10)))
		hash.Write([]byte{0})
		hash.Write([]byte(strconv.FormatUint(issueSelectionRevision(row.UpdatedAt), 10)))
		hash.Write([]byte{0})
	}
	return issueSelectionDigestPrefix + hex.EncodeToString(hash.Sum(nil))
}

func issueSelectionTargets(rows []gormdb.IssueWithCount) []gormdb.CollectionSelectionTarget {
	targets := make([]gormdb.CollectionSelectionTarget, len(rows))
	for index, row := range rows {
		targets[index] = gormdb.CollectionSelectionTarget{ID: strconv.FormatInt(row.ID, 10), ExpectedVersion: issueSelectionRevision(row.UpdatedAt)}
	}
	return targets
}

func issueSelectionRevision(updatedAt time.Time) uint64 {
	return uint64(updatedAt.UTC().Truncate(time.Microsecond).UnixMicro())
}

func issueSelectionUpdatedAt(version uint64) (time.Time, error) {
	if version == 0 || version > uint64(^uint64(0)>>1) {
		return time.Time{}, gormdb.ErrCollectionSelectionInvalid
	}
	return time.UnixMicro(int64(version)).UTC(), nil
}

func encodeIssueSelectionCursor(filter gormdb.CollectionFilter, revision string, request issueSelectionFilter, offset, limit int) (string, error) {
	if !filter.Valid() || !validIssueSelectionRevision(revision) || offset < 0 || limit < 1 || limit > gormdb.CollectionPageMaxSize {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := json.Marshal(issueSelectionCursor{Fingerprint: filter.Fingerprint, Revision: revision, Filter: request, Offset: offset, Limit: limit, Version: issueSelectionCursorV1})
	if err != nil {
		return "", err
	}
	cursor := base64.RawURLEncoding.EncodeToString(payload)
	if len(cursor) == 0 || len(cursor) > 512 {
		return "", gormdb.ErrCollectionSelectionInvalid
	}
	return cursor, nil
}

func decodeIssueSelectionCursor(cursor string) (issueSelectionCursor, error) {
	if len(cursor) == 0 || len(cursor) > 512 {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	var state issueSelectionCursor
	if json.Unmarshal(payload, &state) != nil || state.Version != issueSelectionCursorV1 || state.Offset < 0 || state.Limit < 1 || state.Limit > gormdb.CollectionPageMaxSize || !validIssueSelectionRevision(state.Revision) {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	filter, err := normalizeIssueSelectionFilter(&state.Filter)
	if err != nil || !sameIssueSelectionFilter(filter, state.Filter) || filter.collectionFilter().Fingerprint != state.Fingerprint {
		return issueSelectionCursor{}, gormdb.ErrCollectionSelectionInvalid
	}
	return state, nil
}

func validIssueSelectionRevision(value string) bool {
	if len(value) != len(issueSelectionDigestPrefix)+64 || !strings.HasPrefix(value, issueSelectionDigestPrefix) {
		return false
	}
	for _, character := range value[len(issueSelectionDigestPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func issueOperationSelection(ctx context.Context, store *gormdb.CollectionSelectionStore, scope gormdb.CollectionSelectionScope, request issueSelectionActionScope) (gormdb.CollectionSelection, error) {
	if request.Version < 1 {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	var (
		selection gormdb.CollectionSelection
		err       error
	)
	if request.Kind == gormdb.CollectionSelectionFrozenFilter {
		selection, err = store.Frozen(ctx, scope, request.Token)
	} else if request.Kind == gormdb.CollectionSelectionExplicit || request.Kind == gormdb.CollectionSelectionPage {
		if request.Token != "" {
			return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
		}
		selection, err = store.Current(ctx, scope)
	} else {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionInvalid
	}
	if err != nil {
		return gormdb.CollectionSelection{}, err
	}
	if selection.Kind != request.Kind || selection.Version != request.Version {
		return gormdb.CollectionSelection{}, gormdb.ErrIssueSelectionConflict
	}
	if selection.ReconfirmationRequired {
		return gormdb.CollectionSelection{}, gormdb.ErrCollectionSelectionReconfirmationRequired
	}
	return selection, nil
}

func issueSelectionOperationTargets(selection gormdb.CollectionSelection) ([]gormdb.IssueSelectionTarget, error) {
	excluded := make(map[string]struct{}, len(selection.ExcludedIDs))
	for _, id := range selection.ExcludedIDs {
		excluded[id] = struct{}{}
	}
	targets := make([]gormdb.IssueSelectionTarget, 0, len(selection.Targets))
	for _, target := range selection.Targets {
		if _, found := excluded[target.ID]; found {
			continue
		}
		id, err := strconv.ParseInt(target.ID, 10, 64)
		if err != nil || id <= 0 {
			return nil, gormdb.ErrCollectionSelectionInvalid
		}
		updatedAt, err := issueSelectionUpdatedAt(target.ExpectedVersion)
		if err != nil {
			return nil, err
		}
		targets = append(targets, gormdb.IssueSelectionTarget{ID: id, ExpectedUpdatedAt: updatedAt})
	}
	if len(targets) == 0 {
		return nil, gormdb.ErrCollectionSelectionInvalid
	}
	return targets, nil
}

func issueSelectionAction(request issueSelectionOperationRequest) (gormdb.IssueSelectionAction, error) {
	action := gormdb.IssueSelectionAction{Status: request.Status, Priority: request.Priority, Labels: request.Labels}
	switch request.Action {
	case string(gormdb.IssueSelectionAcknowledge):
		action.Kind = gormdb.IssueSelectionAcknowledge
	case string(gormdb.IssueSelectionStatus):
		action.Kind = gormdb.IssueSelectionStatus
	case string(gormdb.IssueSelectionPriority):
		action.Kind = gormdb.IssueSelectionPriority
	case string(gormdb.IssueSelectionLabels):
		action.Kind = gormdb.IssueSelectionLabels
	default:
		return gormdb.IssueSelectionAction{}, gormdb.ErrIssueInvalidInput
	}
	return action, nil
}

func newIssueSelectionSnapshot(selection gormdb.CollectionSelection) issueSelectionSnapshot {
	result := issueSelectionSnapshot{
		Domain:  issueSelectionDomain,
		Kind:    selection.Kind,
		Version: selection.Version,
	}
	switch selection.Kind {
	case gormdb.CollectionSelectionExplicit:
		result.Targets = append([]gormdb.CollectionSelectionTarget(nil), selection.Targets...)
	case gormdb.CollectionSelectionPage:
		result.Cursor = selection.Cursor
		result.Targets = append([]gormdb.CollectionSelectionTarget(nil), selection.Targets...)
	case gormdb.CollectionSelectionFrozenFilter:
		result.FilterFingerprint = selection.FilterFingerprint
		result.TargetCount = len(selection.Targets) - len(selection.ExcludedIDs)
		result.ExcludedIDs = append([]string(nil), selection.ExcludedIDs...)
		result.Token = selection.Token
		result.ExpiresAt = selection.ExpiresAt.UTC().Format(time.RFC3339Nano)
		result.ReconfirmationRequired = selection.ReconfirmationRequired
		if selection.ReconfirmationRequired {
			result.ReconfirmationReason = string(selection.ReconfirmationReason)
		}
	}
	return result
}

func writeIssueSelectionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gormdb.ErrIssueForbidden), errors.Is(err, gormdb.ErrCollectionSelectionDenied):
		operatorCodeWriteBodyless(w, http.StatusForbidden)
	case errors.Is(err, gormdb.ErrIssueSelectionConflict), errors.Is(err, gormdb.ErrIssueNotFound), errors.Is(err, gormdb.ErrIssueInvalidTransition):
		operatorCodeWriteBodyless(w, http.StatusConflict)
	case errors.Is(err, gormdb.ErrCollectionSelectionReconfirmationRequired):
		operatorCodeWriteBodyless(w, http.StatusPreconditionFailed)
	case errors.Is(err, gormdb.ErrCollectionSelectionInvalid), errors.Is(err, gormdb.ErrIssueInvalidInput):
		operatorCodeWriteBodyless(w, http.StatusBadRequest)
	default:
		operatorCodeWriteBodyless(w, http.StatusInternalServerError)
	}
}
