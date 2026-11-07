package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/models"
)

const principalMemoryQueryMaxLimit = principalmemory.MaxPrincipalQueryLimit

type principalMemoryQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

type principalMemoryQueryResponse struct {
	Principal     string                                    `json:"principal"`
	PrincipalKind string                                    `json:"principal_kind"`
	Project       string                                    `json:"project,omitempty"`
	Domain        string                                    `json:"domain,omitempty"`
	Items         []principalMemoryQueryItem                `json:"items"`
	HiddenCount   *int                                      `json:"hidden_count" extensions:"x-nullable"`
	Audit         principalmemory.PrincipalMemoryQueryAudit `json:"audit"`
	AuditStatus   string                                    `json:"audit_status"`
}

type principalMemoryQueryItem struct {
	ID                 int64     `json:"id"`
	Project            string    `json:"project"`
	Content            string    `json:"content"`
	Tags               []string  `json:"tags" extensions:"x-nullable"`
	OwnerPrincipal     string    `json:"owner_principal"`
	OwnerPrincipalKind string    `json:"owner_principal_kind"`
	AgentVisibility    string    `json:"agent_visibility"`
	Domain             string    `json:"domain"`
	Confidence         float64   `json:"confidence"`
	CreatedAt          time.Time `json:"created_at"`
}

// handlePrincipalMemoryQuery searches principal-owned memories.
// @Summary Search or browse principal memories
// @Description Omit principal to search the enrolled authenticated keycard's self context only, excluding project, global and legacy records. Self requires an enrolled keycard; context may be omitted or exactly self, while project and principal_kind must be absent. An empty principal is not omission. With an explicit principal, context is unsupported and principal_kind defaults to human.
// @Description Self q is literal case-insensitive content substring search, not semantic search. It accepts at most 1024 UTF-8 bytes and 64 distinct letter-or-digit terms of at least two characters. Multiple terms are OR-matched; common English and Russian function words and Russian locating verbs are omitted, and matches rank by term lengths with recency as tie-breaker. A single term is searched literally. A nonempty q with no usable terms returns 400. Omit q to browse. Explicit-principal q instead matches the whole trimmed string as a content substring.
// @Description Authorization, principal, domain and privacy restrictions apply before pagination. hidden_count is null and does not disclose inaccessible records. Cross-principal private widening requires admin and durable audit. Success is JSON; errors are plain text. Authentication accepts X-Auth-Token or a bearer token. Auth-disabled mode is not a privacy boundary and this route does not specially validate an explicit keycard in that mode.
// @Tags Memories
// @Produce json,plain
// @Security ApiKeyAuth
// @Param principal query string false "Principal to query; omit for enrolled self; empty is invalid"
// @Param principal_kind query string false "Explicit principal kind; defaults to human, forbidden for self" Enums(human,agent,service)
// @Param context query string false "Only self is supported, and only when principal is omitted" Enums(self)
// @Param project query string false "Project filter for explicit principal; forbidden for self"
// @Param q query string false "Content query; omit to browse; self search is lexical"
// @Param domain query string false "Domain filter"
// @Param visibility query string false "Visibility filter; defaults to all" Enums(all,private,shared)
// @Param include_private query bool false "Request private rows; cross-principal access requires admin and durable audit"
// @Param limit query int false "Positive page size; defaults to 50; values above 500 are capped at 500" minimum(1)
// @Param offset query int false "Non-negative result offset; defaults to 0" minimum(0)
// @Param session_id query string false "Optional audit provenance"
// @Success 200 {object} principalMemoryQueryResponse "Visible results and audit metadata; use card read for full context and revision"
// @Failure 400 {string} string "Invalid selectors, query or pagination"
// @Failure 401 {string} string "Authentication required"
// @Failure 403 {string} string "Self requires an enrolled keycard or cross-principal private access is denied"
// @Failure 500 {string} string "Query or required audit failed"
// @Failure 503 {string} string "Query service or self context unavailable"
// @Router /api/memories/principal [get]
func (s *Service) handlePrincipalMemoryQuery(w http.ResponseWriter, r *http.Request) {
	queryService := s.currentPrincipalMemoryQueryService()
	if queryService == nil {
		http.Error(w, "principal memory query service not available", http.StatusServiceUnavailable)
		return
	}

	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		http.Error(w, "invalid query parameters", http.StatusBadRequest)
		return
	}
	_, principalSupplied := q["principal"]
	self := !principalSupplied
	ownerPrincipal := strings.TrimSpace(q.Get("principal"))
	ownerPrincipalKind := strings.TrimSpace(strings.ToLower(q.Get("principal_kind")))
	var queryTerms []string
	queryText := strings.TrimSpace(q.Get("q"))
	if self {
		contexts, contextSupplied := q["context"]
		_, projectSupplied := q["project"]
		_, kindSupplied := q["principal_kind"]
		if projectSupplied || kindSupplied || (contextSupplied && (len(contexts) != 1 || contexts[0] != "self")) {
			http.Error(w, "invalid memory context", http.StatusBadRequest)
			return
		}
		if s.memoryStore == nil {
			http.Error(w, "memory context unavailable", http.StatusServiceUnavailable)
			return
		}
		owning, err := s.resolveMemoryOwningContext(r, models.MemoryContextSelf, true, "", false)
		if err != nil {
			writeMemoryContextError(w, err)
			return
		}
		ownerPrincipal, ownerPrincipalKind = owning.Principal, owning.PrincipalKind
		if len(q.Get("q")) > 1024 {
			http.Error(w, "query is too long", http.StatusBadRequest)
			return
		}
		if q.Get("q") != "" {
			seen := make(map[string]struct{})
			for _, field := range strings.FieldsFunc(queryText, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
				term := strings.ToLower(field)
				if utf8.RuneCountInString(term) < 2 {
					continue
				}
				if _, exists := seen[term]; exists {
					continue
				}
				seen[term] = struct{}{}
				queryTerms = append(queryTerms, term)
				if len(queryTerms) > 64 {
					http.Error(w, "query has too many distinct terms", http.StatusBadRequest)
					return
				}
			}
			if len(queryTerms) > 1 {
				filtered := queryTerms[:0]
				for _, term := range queryTerms {
					if !selfQuestionStopword(term) {
						filtered = append(filtered, term)
					}
				}
				queryTerms = filtered
			}
			if len(queryTerms) == 0 {
				http.Error(w, "query has no usable terms", http.StatusBadRequest)
				return
			}
			queryText = ""
		}
	} else {
		if _, contextSupplied := q["context"]; contextSupplied {
			http.Error(w, "context is unsupported with principal", http.StatusBadRequest)
			return
		}
		if ownerPrincipal == "" {
			http.Error(w, "principal is required", http.StatusBadRequest)
			return
		}
		if ownerPrincipalKind == "" {
			ownerPrincipalKind = "human"
		}
		if !auth.IsValidPrincipalKind(auth.PrincipalKind(ownerPrincipalKind)) {
			http.Error(w, "principal_kind must be one of human, agent, service", http.StatusBadRequest)
			return
		}
	}

	visibility, err := parsePrincipalQueryVisibility(q.Get("visibility"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit, err := parsePrincipalQueryLimit(q.Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	offset, err := parsePrincipalQueryOffset(q.Get("offset"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	includePrivate, err := parseOptionalBool(q.Get("include_private"))
	if err != nil {
		http.Error(w, "include_private must be a boolean", http.StatusBadRequest)
		return
	}

	caller, callerIsAdmin := principalQueryCaller(r.Context())
	result, err := queryService.Query(r.Context(), principalmemory.PrincipalMemoryQueryRequest{
		Project:            strings.TrimSpace(q.Get("project")),
		SelfContextOnly:    self,
		RankQueryTerms:     self && len(queryTerms) > 0,
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerPrincipalKind,
		Query:              queryText,
		QueryTerms:         queryTerms,
		AgentVisibility:    visibility,
		IncludePrivate:     includePrivate,
		Domain:             strings.TrimSpace(q.Get("domain")),
		Limit:              limit,
		Offset:             offset,
		SourceSessionID:    strings.TrimSpace(q.Get("session_id")),
	})
	if err != nil {
		if errors.Is(err, principalmemory.ErrCrossPrincipalPrivateDenied) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, "principal memory query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, principalMemoryQueryHTTPResponse(result))
}

func selfQuestionStopword(term string) bool {
	if _, ok := experienceQueryStopwords[term]; ok {
		return true
	}
	// Common locating verbs add little subject information in multiword questions.
	switch term {
	case "находится", "находятся", "находился", "находилась", "расположен", "расположена", "расположено", "расположены", "найти", "искать", "располагается", "располагаются":
		return true
	}
	switch term {
	case "is", "if", "do", "in", "on", "it", "of", "to", "be", "as", "at", "by", "or", "an",
		"где", "для", "что", "такое", "как", "когда", "почему", "какой", "какая", "какие", "какое", "кто", "чем", "чего", "чему", "который", "которая", "которые", "это", "этот", "эта", "эти", "его", "её", "их", "мне", "мой", "моя", "мои", "есть", "был", "была", "были", "при", "под", "над", "без", "или", "так":
		return true
	}
	return false
}

func (s *Service) currentPrincipalMemoryQueryService() principalMemoryQueryService {
	s.initMu.RLock()
	queryService := s.principalMemoryQueryService
	s.initMu.RUnlock()
	return queryService
}

func principalQueryCaller(ctx context.Context) (principalmemory.PrincipalRef, bool) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return principalmemory.PrincipalRef{}, false
	}
	principal, kind, hasOwner := id.MemoryOwner()
	if !hasOwner {
		if id.IsAdmin() {
			return principalmemory.PrincipalRef{Principal: "system", PrincipalKind: "service"}, true
		}
		return principalmemory.PrincipalRef{}, id.IsAdmin()
	}
	return principalmemory.PrincipalRef{Principal: principal, PrincipalKind: kind}, id.IsAdmin()
}

func parsePrincipalQueryLimit(raw string) (int, error) {
	if raw == "" {
		return principalmemory.DefaultPrincipalQueryLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if n > principalMemoryQueryMaxLimit {
		return principalMemoryQueryMaxLimit, nil
	}
	return n, nil
}

func parsePrincipalQueryOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("offset must be a non-negative integer")
	}
	return n, nil
}

func parseOptionalBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	return strconv.ParseBool(raw)
}

func parsePrincipalQueryVisibility(raw string) (string, error) {
	visibility := strings.TrimSpace(strings.ToLower(raw))
	switch visibility {
	case "", "all":
		return "", nil
	case models.AgentVisibilityPrivate, models.AgentVisibilityShared:
		return visibility, nil
	default:
		return "", fmt.Errorf("visibility must be one of private, shared, all")
	}
}

func principalMemoryQueryHTTPResponse(result *principalmemory.PrincipalMemoryQueryResult) principalMemoryQueryResponse {
	resp := principalMemoryQueryResponse{
		Items:       make([]principalMemoryQueryItem, 0),
		AuditStatus: principalmemory.AuditStatusNotRequired,
		Audit: principalmemory.PrincipalMemoryQueryAudit{
			Action: principalmemory.AuditActionQuery,
		},
	}
	if result == nil {
		return resp
	}
	resp.Principal = result.Principal
	resp.PrincipalKind = result.PrincipalKind
	resp.Project = result.Project
	resp.Domain = result.Domain
	resp.HiddenCount = result.HiddenCount
	resp.AuditStatus = result.AuditStatus
	resp.Audit = result.Audit
	if resp.Audit.Action == "" {
		resp.Audit.Action = principalmemory.AuditActionQuery
	}
	resp.Items = make([]principalMemoryQueryItem, 0, len(result.Items))
	for _, item := range result.Items {
		resp.Items = append(resp.Items, principalMemoryQueryItem{
			ID:                 item.ID,
			Project:            item.Project,
			Content:            item.Content,
			Tags:               append([]string(nil), item.Tags...),
			OwnerPrincipal:     item.OwnerPrincipal,
			OwnerPrincipalKind: item.OwnerPrincipalKind,
			AgentVisibility:    item.AgentVisibility,
			Domain:             item.Domain,
			Confidence:         item.Confidence,
			CreatedAt:          item.CreatedAt,
		})
	}
	return resp
}
