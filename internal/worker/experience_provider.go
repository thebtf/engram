package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/thebtf/engram/internal/auth"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

const experienceProjectionFetchLimit = 500

type experiencePrincipalQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

type memoryExperienceProvider struct {
	querySvc experiencePrincipalQueryService
}

func newMemoryExperienceProvider(querySvc experiencePrincipalQueryService) *memoryExperienceProvider {
	return &memoryExperienceProvider{querySvc: querySvc}
}

func (p *memoryExperienceProvider) QueryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error) {
	service, err := p.serviceForRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	return service.QueryExperience(ctx, request)
}

func (p *memoryExperienceProvider) QueryExperienceWithArchiveEvidence(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []experiencehistory.ArchiveEvidenceEntry, error) {
	service, err := p.serviceForRequest(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	return service.QueryExperienceWithArchiveEvidence(ctx, request)
}

func (p *memoryExperienceProvider) QueryExperienceDetail(ctx context.Context, request experiencehistory.HistoryDetailRequest) (cognitive.ExperienceResponse, []experiencehistory.ArchiveEvidenceEntry, bool, error) {
	service, err := p.serviceForRequest(ctx, cognitive.ExperienceQueryRequest{
		Project:               request.Project,
		Principal:             request.Principal,
		Domain:                request.Domain,
		Query:                 request.ExperienceID,
		CurrentContext:        request.CurrentContext,
		ArchiveTriggerClasses: request.ArchiveTriggerClasses,
		Limit:                 experiencehistory.MaxQueryLimit,
	})
	if err != nil {
		return cognitive.ExperienceResponse{}, nil, false, err
	}
	return service.QueryExperienceDetail(ctx, request)
}

// detailMemoryID extracts a numeric memory id from an experience detail id such
// as "memory:7" or a bare "7". It returns (id, true) when the id addresses a
// single stored memory, so the projection can fetch that exact row by id instead
// of scanning the newest-N window. Any other shape returns (0, false) and the
// caller falls back to the general term/bounded path.
func detailMemoryID(experienceID string) (int64, bool) {
	raw := strings.TrimSpace(experienceID)
	if raw == "" {
		return 0, false
	}
	raw = strings.TrimPrefix(raw, "memory:")
	raw = strings.TrimSpace(raw)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// queryContentTerms splits a free-text experience query into significant lower-
// cased content terms for OR-narrowing at the SQL layer. Tokens shorter than 3
// runes and a small set of generic stopwords are dropped so the narrowing keeps
// recall without collapsing to a full-phrase predicate. An empty result tells
// serviceForRequest to fall back to the bounded newest-N fetch.
func queryContentTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		t := strings.ToLower(strings.TrimSpace(f))
		if len([]rune(t)) < 3 {
			continue
		}
		if _, stop := experienceQueryStopwords[t]; stop {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}
	return terms
}

var experienceQueryStopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "that": {}, "this": {},
	"from": {}, "into": {}, "was": {}, "are": {}, "were": {}, "has": {},
	"have": {}, "had": {}, "not": {}, "but": {}, "any": {}, "all": {},
	"can": {}, "will": {}, "should": {}, "would": {}, "when": {}, "what": {},
}

func (p *memoryExperienceProvider) serviceForRequest(ctx context.Context, request cognitive.ExperienceQueryRequest) (*experiencehistory.Service, error) {
	if p == nil || p.querySvc == nil {
		return nil, fmt.Errorf("experience provider: principal memory query service is not configured")
	}
	project := strings.TrimSpace(request.Project)
	if project == "" {
		return experiencehistory.NewService(nil), nil
	}
	caller, callerIsAdmin := principalMemoryQueryCallerFromContext(ctx)
	ownerPrincipal := strings.TrimSpace(request.Principal)
	ownerKind := ""
	if ownerPrincipal != "" && caller.Principal == ownerPrincipal {
		ownerKind = caller.PrincipalKind
	}
	queryReq := principalmemory.PrincipalMemoryQueryRequest{
		Project:            project,
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerKind,
		Query:              "",
		Domain:             strings.TrimSpace(request.Domain),
		Limit:              experienceProjectionFetchLimit,
	}
	// Detail-by-id: a "memory:<id>" experience id fetches that exact row directly
	// (under the same access-policy gating) instead of scanning the newest-N
	// window, so a target older than the projection limit is still retrievable.
	if id, ok := detailMemoryID(request.Query); ok {
		queryReq.IDs = []int64{id}
	} else if terms := queryContentTerms(request.Query); len(terms) > 0 {
		// General query: narrow by ORed content terms (recall-preserving, no
		// full-phrase cliff, no recency cliff) instead of an unfiltered fetch.
		queryReq.QueryTerms = terms
	}
	result, err := p.querySvc.Query(ctx, queryReq)
	if err != nil {
		return nil, err
	}
	return experiencehistory.NewService(memoriesToExperienceResponses(principalQueryMemories(result))), nil
}

func principalMemoryQueryCallerFromContext(ctx context.Context) (principalmemory.PrincipalRef, bool) {
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

func principalQueryMemories(result *principalmemory.PrincipalMemoryQueryResult) []*models.Memory {
	if result == nil || len(result.Items) == 0 {
		return nil
	}
	memories := make([]*models.Memory, 0, len(result.Items))
	for _, item := range result.Items {
		memories = append(memories, item.Memory())
	}
	return memories
}

func memoriesToExperienceResponses(memories []*models.Memory) []cognitive.ExperienceResponse {
	responses := make([]cognitive.ExperienceResponse, 0, len(memories))
	for _, memory := range memories {
		if memory == nil || strings.TrimSpace(memory.Content) == "" {
			continue
		}
		responses = append(responses, memoryToExperienceResponse(memory))
	}
	return responses
}

func memoryToExperienceResponse(memory *models.Memory) cognitive.ExperienceResponse {
	attribution := cognitive.ExperienceSourceAttribution{
		Kind:      "memory",
		ID:        strconv.FormatInt(memory.ID, 10),
		Project:   memory.Project,
		CreatedAt: memory.CreatedAt,
	}
	if len(memory.SourceSessions) > 0 {
		attribution.SessionID = memory.SourceSessions[0]
	}
	return cognitive.ExperienceResponse{
		Source:        cognitive.ExperienceSourceProjection,
		StorageOrigin: cognitive.ExperienceSourceProjection,
		Situation:     "stored memory",
		TimeSpan: cognitive.ExperienceTimeSpan{
			StartedAt: memory.CreatedAt,
			EndedAt:   memory.UpdatedAt,
		},
		Outcome: memory.Status,
		Lesson:  memory.Content,
		Applicability: cognitive.ExperienceApplicability{
			State:           cognitive.ExperienceApplicabilityUncertain,
			Rationale:       "projected from stored memory; current_context decides applicability",
			RequiredContext: []string{"current_context"},
			Confidence:      "low",
		},
		Provenance:        []cognitive.ExperienceSourceAttribution{attribution},
		SourceAttribution: []cognitive.ExperienceSourceAttribution{attribution},
	}
}
