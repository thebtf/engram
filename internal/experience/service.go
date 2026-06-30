package experience

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	DefaultQueryLimit          = 5
	MaxQueryLimit              = 10
	MaxArchiveResurfacingLimit = 10
	MaxArchiveEvidenceEntries  = 100
)

// ArchiveSource supplies cold historical experience only after a named trigger.
type ArchiveSource interface {
	QueryArchiveExperience(ctx context.Context, request cognitive.ExperienceQueryRequest, triggers []cognitive.ExperienceArchiveTriggerClass, limit int) ([]cognitive.ExperienceResponse, error)
}

// ArchiveEvidenceEntry records one trigger-gated archive resurfacing decision.
type ArchiveEvidenceEntry struct {
	TriggerClasses []cognitive.ExperienceArchiveTriggerClass `json:"trigger_classes"`
	RequestedLimit int                                       `json:"requested_limit"`
	Returned       int                                       `json:"returned"`
	Status         string                                    `json:"status"`
	Reason         string                                    `json:"reason,omitempty"`
}

// Service returns first-class experience responses from projected candidates.
type Service struct {
	candidates      []cognitive.ExperienceResponse
	archive         ArchiveSource
	archiveEvidence []ArchiveEvidenceEntry
	archiveMu       sync.Mutex
}

var _ cognitive.ExperienceProvider = (*Service)(nil)

// NewService creates a projection-backed experience provider.
func NewService(candidates []cognitive.ExperienceResponse) *Service {
	return &Service{candidates: cloneResponses(candidates)}
}

// NewServiceWithArchive creates a projection-backed provider with an explicit
// archive source. The source is used only when request trigger classes are set.
func NewServiceWithArchive(candidates []cognitive.ExperienceResponse, archive ArchiveSource) *Service {
	return &Service{
		candidates: cloneResponses(candidates),
		archive:    archive,
	}
}

// QueryExperience returns bounded historical/causal lessons for an explicit
// experience request. It does not call hot-memory retrieval.
func (s *Service) QueryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("experience service is not configured")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	triggers, err := NormalizeArchiveTriggerClasses(request.ArchiveTriggerClasses)
	if err != nil {
		return nil, err
	}
	limit := normalizeLimit(request.Limit)
	candidates := cloneResponses(s.candidates)
	if len(triggers) > 0 && s.archive != nil {
		archiveLimit := archiveLimit(limit)
		archiveRequest := request
		archiveRequest.ArchiveTriggerClasses = triggers
		archiveRequest.Limit = archiveLimit
		archiveItems, err := s.archive.QueryArchiveExperience(ctx, archiveRequest, triggers, archiveLimit)
		if err != nil {
			return nil, err
		}
		if len(archiveItems) > archiveLimit {
			archiveItems = archiveItems[:archiveLimit]
		}
		for i := range archiveItems {
			archiveItems[i] = cloneResponse(archiveItems[i])
			archiveItems[i].ArchiveTriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...)
		}
		s.recordArchiveEvidence(ArchiveEvidenceEntry{
			TriggerClasses: append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
			RequestedLimit: archiveLimit,
			Returned:       len(archiveItems),
			Status:         "archive_resurfaced",
		})
		candidates = append(candidates, archiveItems...)
	}
	terms := requestTerms(request)
	type scoredCandidate struct {
		item  cognitive.ExperienceResponse
		score int
		index int
	}
	scored := make([]scoredCandidate, 0, len(candidates))
	for i, candidate := range candidates {
		if !projectMatches(request.Project, candidate.SourceAttribution) {
			continue
		}
		score := relevanceScore(terms, candidate)
		if len(terms) > 0 && score == 0 {
			continue
		}
		item := cloneResponse(candidate)
		item.Applicability = classifyApplicability(request, item, score)
		scored = append(scored, scoredCandidate{item: item, score: score, index: i})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].index < scored[j].index
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	results := make([]cognitive.ExperienceResponse, 0, len(scored))
	for _, candidate := range scored {
		results = append(results, candidate.item)
	}
	return results, nil
}

// ArchiveEvidence returns a snapshot of archive resurfacing evidence.
func (s *Service) ArchiveEvidence() []ArchiveEvidenceEntry {
	if s == nil {
		return nil
	}
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	out := make([]ArchiveEvidenceEntry, 0, len(s.archiveEvidence))
	for _, entry := range s.archiveEvidence {
		entry.TriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), entry.TriggerClasses...)
		out = append(out, entry)
	}
	return out
}

func (s *Service) recordArchiveEvidence(entry ArchiveEvidenceEntry) {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	s.archiveEvidence = append(s.archiveEvidence, entry)
	if len(s.archiveEvidence) > MaxArchiveEvidenceEntries {
		start := len(s.archiveEvidence) - MaxArchiveEvidenceEntries
		s.archiveEvidence = append([]ArchiveEvidenceEntry(nil), s.archiveEvidence[start:]...)
	}
}

var allowedArchiveTriggerClasses = []cognitive.ExperienceArchiveTriggerClass{
	cognitive.ExperienceArchiveTriggerWhyChanged,
	cognitive.ExperienceArchiveTriggerRegression,
	cognitive.ExperienceArchiveTriggerRollback,
	cognitive.ExperienceArchiveTriggerOldDecisionRevisit,
	cognitive.ExperienceArchiveTriggerSimilarFailure,
}

func AllowedArchiveTriggerClasses() []cognitive.ExperienceArchiveTriggerClass {
	return append([]cognitive.ExperienceArchiveTriggerClass(nil), allowedArchiveTriggerClasses...)
}

func NormalizeArchiveTriggerClasses(classes []cognitive.ExperienceArchiveTriggerClass) ([]cognitive.ExperienceArchiveTriggerClass, error) {
	if len(classes) == 0 {
		return nil, nil
	}
	seen := make(map[cognitive.ExperienceArchiveTriggerClass]bool, len(classes))
	for _, class := range classes {
		if !validArchiveTriggerClass(class) {
			return nil, fmt.Errorf("invalid archive trigger class: %s", class)
		}
		seen[class] = true
	}
	normalized := make([]cognitive.ExperienceArchiveTriggerClass, 0, len(seen))
	for _, class := range allowedArchiveTriggerClasses {
		if seen[class] {
			normalized = append(normalized, class)
		}
	}
	return normalized, nil
}

func validArchiveTriggerClass(class cognitive.ExperienceArchiveTriggerClass) bool {
	return slices.Contains(allowedArchiveTriggerClasses, class)
}

func archiveLimit(requestLimit int) int {
	limit := normalizeLimit(requestLimit)
	if limit > MaxArchiveResurfacingLimit {
		return MaxArchiveResurfacingLimit
	}
	return limit
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultQueryLimit
	}
	if limit > MaxQueryLimit {
		return MaxQueryLimit
	}
	return limit
}

func requestTerms(request cognitive.ExperienceQueryRequest) []string {
	return uniqueTerms(request.Query + " " + request.CurrentContext)
}

func relevanceScore(terms []string, candidate cognitive.ExperienceResponse) int {
	if len(terms) == 0 {
		return 1
	}
	candidateTerms := candidateSearchTermSet(candidate)
	score := 0
	for _, term := range terms {
		if _, ok := candidateTerms[term]; ok {
			score++
		}
	}
	return score
}

func candidateSearchTermSet(candidate cognitive.ExperienceResponse) map[string]struct{} {
	parts := []string{
		candidate.Lesson,
		candidate.Applicability.Rationale,
		string(candidate.Applicability.State),
		string(candidate.Source),
	}
	for _, anti := range candidate.AntiApplicability {
		parts = append(parts, anti.Condition, anti.Rationale)
	}
	for _, attribution := range candidate.SourceAttribution {
		parts = append(parts, attribution.Kind, attribution.ID, attribution.Project, attribution.SessionID)
	}
	for _, trigger := range candidate.ArchiveTriggerClasses {
		parts = append(parts, string(trigger))
	}
	return termSet(uniqueTerms(strings.Join(parts, " ")))
}

func classifyApplicability(request cognitive.ExperienceQueryRequest, candidate cognitive.ExperienceResponse, score int) cognitive.ExperienceApplicability {
	for _, anti := range candidate.AntiApplicability {
		if antiApplicabilityMatches(anti.Condition, request) {
			rationale := strings.TrimSpace(anti.Rationale)
			if rationale == "" {
				rationale = fmt.Sprintf("anti-applicability condition matched: %s", strings.TrimSpace(anti.Condition))
			}
			return cognitive.ExperienceApplicability{
				State:     cognitive.ExperienceApplicabilityBlocked,
				Rationale: rationale,
			}
		}
	}
	if strings.TrimSpace(request.CurrentContext) == "" {
		return cognitive.ExperienceApplicability{
			State:     cognitive.ExperienceApplicabilityUncertain,
			Rationale: "current_context is required before this experience can be silently reused",
		}
	}
	if score <= 0 {
		return cognitive.ExperienceApplicability{
			State:     cognitive.ExperienceApplicabilityUncertain,
			Rationale: "experience did not match the request context strongly enough for automatic reuse",
		}
	}
	if candidate.Applicability.State != "" && strings.TrimSpace(candidate.Applicability.Rationale) != "" {
		return candidate.Applicability
	}
	return cognitive.ExperienceApplicability{
		State:     cognitive.ExperienceApplicabilityApplies,
		Rationale: "experience matched the request context and no anti-applicability condition matched",
	}
}

func antiApplicabilityMatches(condition string, request cognitive.ExperienceQueryRequest) bool {
	conditionTerms := uniqueTerms(condition)
	if len(conditionTerms) == 0 {
		return false
	}
	contextTerms := termSet(uniqueTerms(request.Query + " " + request.CurrentContext))
	for _, term := range conditionTerms {
		if _, ok := contextTerms[term]; !ok {
			return false
		}
	}
	return true
}

func projectMatches(project string, attributions []cognitive.ExperienceSourceAttribution) bool {
	project = strings.TrimSpace(project)
	if project == "" {
		return true
	}
	for _, attribution := range attributions {
		attrProject := strings.TrimSpace(attribution.Project)
		if attrProject == "" {
			continue
		}
		if attrProject == project {
			return true
		}
	}
	return false
}

func uniqueTerms(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool, len(fields))
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len(field) < 3 || stopWord(field) || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func termSet(terms []string) map[string]struct{} {
	set := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		set[term] = struct{}{}
	}
	return set
}

func stopWord(term string) bool {
	switch term {
	case "and", "the", "for", "this", "that", "with", "from", "into", "must", "not", "may", "can", "was", "were", "are":
		return true
	default:
		return false
	}
}

func cloneResponses(items []cognitive.ExperienceResponse) []cognitive.ExperienceResponse {
	cloned := make([]cognitive.ExperienceResponse, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, cloneResponse(item))
	}
	return cloned
}

func cloneResponse(item cognitive.ExperienceResponse) cognitive.ExperienceResponse {
	item.AntiApplicability = append([]cognitive.ExperienceAntiApplicability(nil), item.AntiApplicability...)
	item.SourceAttribution = append([]cognitive.ExperienceSourceAttribution(nil), item.SourceAttribution...)
	item.ArchiveTriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), item.ArchiveTriggerClasses...)
	return item
}
