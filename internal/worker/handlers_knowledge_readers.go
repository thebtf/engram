package worker

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/lifecycle"
)

type knowledgePage struct {
	project string
	query   string
	filters []string
	types   []string
	offset  int
	limit   int
}

func boundedKnowledgeText(value string, maxBytes int) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func parseKnowledgeProject(params url.Values) (string, error) {
	project := params.Get("project")
	if project == "" || strings.TrimSpace(project) != project || !boundedKnowledgeText(project, 256) {
		return "", fmt.Errorf("an exact canonical project ID is required")
	}
	return project, nil
}

func parseKnowledgePage(r *http.Request) (knowledgePage, error) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return knowledgePage{}, fmt.Errorf("invalid reader query parameters")
	}
	project, err := parseKnowledgeProject(params)
	if err != nil {
		return knowledgePage{}, err
	}
	page := knowledgePage{project: project, limit: 50}
	rawQuery := params.Get("query")
	if !boundedKnowledgeText(rawQuery, 1024) {
		return page, fmt.Errorf("query must be valid UTF-8 text without controls, at most 1024 bytes")
	}
	page.query = strings.TrimSpace(rawQuery)
	if r.URL.Path == "/api/memories" {
		values := params["filter"]
		if len(values) > 6 {
			return page, fmt.Errorf("at most six filter values are allowed")
		}
		count := 0
		for _, filter := range values {
			switch filter {
			case "all", "active", "flagged", "stale", "low", "chain":
			default:
				return page, fmt.Errorf("each filter must be all, active, flagged, stale, low or chain")
			}
			duplicate := false
			for _, selected := range values[:count] {
				if selected == filter {
					duplicate = true
					break
				}
			}
			if !duplicate {
				values[count] = filter
				count++
			}
		}
		page.filters = values[:count]
	}
	typeField := ""
	switch r.URL.Path {
	case "/api/memories":
		typeField = "epistemic_type"
	case "/api/documents":
		typeField = "doc_type"
	}
	if typeField != "" {
		values := params[typeField]
		if len(values) > 6 {
			return page, fmt.Errorf("at most six %s values are allowed", typeField)
		}
		count := 0
		for _, value := range values {
			if !boundedKnowledgeText(value, 128) {
				return page, fmt.Errorf("invalid %s value", typeField)
			}
			if typeField == "epistemic_type" {
				if !lifecycle.ValidEpistemicType(value) {
					return page, fmt.Errorf("each epistemic_type must be fact, experience, opinion or observation")
				}
			} else {
				value = strings.TrimSpace(value)
				if value == "" {
					return page, fmt.Errorf("doc_type must not be blank")
				}
			}
			duplicate := false
			for _, selected := range values[:count] {
				if selected == value {
					duplicate = true
					break
				}
			}
			if !duplicate {
				values[count] = value
				count++
			}
		}
		page.types = values[:count]
	}
	for _, field := range []string{"offset", "limit"} {
		if !params.Has(field) {
			continue
		}
		value, err := strconv.Atoi(params.Get(field))
		if err != nil || value < 0 || (field == "limit" && (value < 1 || value > 500)) {
			return page, fmt.Errorf("offset must be nonnegative and limit must be 1..500")
		}
		if field == "offset" {
			page.offset = value
		} else {
			page.limit = value
		}
	}
	return page, nil
}

func (s *Service) authorizeKnowledgeProject(w http.ResponseWriter, r *http.Request, project string) bool {
	if identity, ok := auth.IdentityFrom(r.Context()); ok && !hapHTTPRouteAllowed(identity, r.Method, r.URL.Path) {
		writeDocumentError(w, http.StatusForbidden, "project read forbidden")
		return false
	}
	var exists bool
	var err error
	if s.knowledgeProjectExistsSeam != nil {
		exists, err = s.knowledgeProjectExistsSeam(r.Context(), project)
	} else if s.sessionStore != nil {
		exists, err = s.sessionStore.CanonicalProjectExists(r.Context(), project)
	} else {
		writeDocumentError(w, http.StatusServiceUnavailable, "project catalog unavailable")
		return false
	}
	if err != nil {
		writeDocumentError(w, http.StatusInternalServerError, "project read unavailable")
		return false
	}
	if !exists {
		writeDocumentError(w, http.StatusForbidden, "project read forbidden")
		return false
	}
	return true
}

func knowledgeHasMore(total int64, offset, length int) bool {
	return int64(offset) < total && int64(length) < total-int64(offset)
}
