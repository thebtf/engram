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
	TriggerClasses           []cognitive.ExperienceArchiveTriggerClass `json:"trigger_classes"`
	CallerPrincipal          string                                    `json:"caller_principal"`
	Project                  string                                    `json:"project"`
	SessionIDs               []string                                  `json:"session_ids"`
	RequestedLimit           int                                       `json:"requested_limit"`
	Returned                 int                                       `json:"returned"`
	ExperienceRetrievalRan   bool                                      `json:"experience_retrieval_ran"`
	AntiApplicabilityBlocked bool                                      `json:"anti_applicability_blocked"`
	EvidenceRefs             []string                                  `json:"evidence_refs"`
	Status                   string                                    `json:"status"`
	Reason                   string                                    `json:"reason"`
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
	results, _, err := s.queryExperience(ctx, request)
	return results, err
}

// QueryExperienceWithArchiveEvidence returns the per-request archive evidence
// created while serving this query. The returned evidence is not read from the
// process-wide evidence ring, so concurrent experience reads cannot borrow one
// another's archive trace.
func (s *Service) QueryExperienceWithArchiveEvidence(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []ArchiveEvidenceEntry, error) {
	return s.queryExperience(ctx, request)
}

// QueryExperienceDetail performs an exact adapter/provenance id lookup before
// relevance limiting. It keeps the same project scope, applicability, and named
// archive trigger rules as QueryExperience.
func (s *Service) QueryExperienceDetail(ctx context.Context, request HistoryDetailRequest) (cognitive.ExperienceResponse, []ArchiveEvidenceEntry, bool, error) {
	if s == nil {
		return cognitive.ExperienceResponse{}, nil, false, fmt.Errorf("experience service is not configured")
	}
	select {
	case <-ctx.Done():
		return cognitive.ExperienceResponse{}, nil, false, ctx.Err()
	default:
	}

	request.Project = strings.TrimSpace(request.Project)
	request.Principal = strings.TrimSpace(request.Principal)
	request.Domain = strings.TrimSpace(request.Domain)
	request.ExperienceID = strings.TrimSpace(request.ExperienceID)
	request.CurrentContext = strings.TrimSpace(request.CurrentContext)
	triggers, err := NormalizeArchiveTriggerClasses(request.ArchiveTriggerClasses)
	if err != nil {
		return cognitive.ExperienceResponse{}, nil, false, err
	}
	queryRequest := cognitive.ExperienceQueryRequest{
		Project:               request.Project,
		Principal:             request.Principal,
		Domain:                request.Domain,
		CurrentContext:        request.CurrentContext,
		ArchiveTriggerClasses: triggers,
		Limit:                 MaxQueryLimit,
	}
	candidates := cloneResponses(s.candidates)
	terms := requestTerms(queryRequest)
	for _, candidate := range candidates {
		if !projectMatches(request.Project, candidate.SourceAttribution, candidate.Provenance) {
			continue
		}
		item := cloneResponse(candidate)
		item.Applicability = classifyApplicability(queryRequest, item, relevanceScore(terms, item))
		if !experienceMatchesID(item, request.ExperienceID) {
			continue
		}
		if len(triggers) > 0 {
			perCallEvidence := ArchiveEvidenceEntry{
				TriggerClasses:         append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
				CallerPrincipal:        request.Principal,
				Project:                request.Project,
				RequestedLimit:         archiveLimit(MaxQueryLimit),
				Status:                 "archive_skipped",
				Reason:                 "exact projected experience detail found before archive lookup",
				EvidenceRefs:           provenanceRefs(item),
				ExperienceRetrievalRan: false,
			}
			return item, []ArchiveEvidenceEntry{cloneArchiveEvidenceEntry(perCallEvidence)}, true, nil
		}
		return item, nil, true, nil
	}
	var perCallEvidence []ArchiveEvidenceEntry
	var archiveEvidence ArchiveEvidenceEntry
	archiveEvidencePending := false
	archiveStart := -1
	archiveEnd := -1
	if len(triggers) > 0 {
		archiveLimit := archiveLimit(MaxQueryLimit)
		archiveEvidence = ArchiveEvidenceEntry{
			TriggerClasses:  append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
			CallerPrincipal: request.Principal,
			Project:         request.Project,
			RequestedLimit:  archiveLimit,
			Status:          "archive_unavailable",
			Reason:          "named archive trigger supplied but archive source is not configured",
			EvidenceRefs:    archiveTriggerEvidenceRefs(triggers),
		}
		if s.archive != nil {
			archiveRequest := queryRequest
			archiveRequest.ArchiveTriggerClasses = triggers
			archiveRequest.Limit = archiveLimit
			archiveItems, err := s.archive.QueryArchiveExperience(ctx, archiveRequest, triggers, archiveLimit)
			if err != nil {
				archiveEvidence.Returned = 0
				archiveEvidence.ExperienceRetrievalRan = true
				archiveEvidence.AntiApplicabilityBlocked = false
				archiveEvidence.Status = "archive_error"
				archiveEvidence.Reason = "archive source returned error"
				if len(archiveEvidence.EvidenceRefs) == 0 {
					archiveEvidence.EvidenceRefs = archiveTriggerEvidenceRefs(triggers)
				}
				s.recordArchiveEvidence(archiveEvidence)
				return cognitive.ExperienceResponse{}, []ArchiveEvidenceEntry{cloneArchiveEvidenceEntry(archiveEvidence)}, false, err
			}
			if len(archiveItems) > archiveLimit {
				archiveItems = archiveItems[:archiveLimit]
			}
			filteredArchiveItems := archiveItems[:0]
			for _, archiveItem := range archiveItems {
				item := cloneResponse(archiveItem)
				item.ArchiveTriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...)
				if !projectMatches(request.Project, item.SourceAttribution, item.Provenance) {
					continue
				}
				filteredArchiveItems = append(filteredArchiveItems, item)
			}
			archiveStart = len(candidates)
			candidates = append(candidates, filteredArchiveItems...)
			archiveEnd = len(candidates)
			archiveEvidence.ExperienceRetrievalRan = true
			archiveEvidencePending = true
		} else {
			s.recordArchiveEvidence(archiveEvidence)
			perCallEvidence = append(perCallEvidence, cloneArchiveEvidenceEntry(archiveEvidence))
		}
	}
	terms = requestTerms(queryRequest)
	for i, candidate := range candidates {
		if !projectMatches(request.Project, candidate.SourceAttribution, candidate.Provenance) {
			continue
		}
		item := cloneResponse(candidate)
		item.Applicability = classifyApplicability(queryRequest, item, relevanceScore(terms, item))
		if !experienceMatchesID(item, request.ExperienceID) {
			continue
		}
		if archiveEvidencePending && i >= archiveStart && i < archiveEnd {
			archiveEvidence.SessionIDs, archiveEvidence.EvidenceRefs = archiveEvidenceScope([]cognitive.ExperienceResponse{item})
			archiveEvidence.Returned = 1
			archiveEvidence.AntiApplicabilityBlocked = archiveAntiApplicabilityBlocked(queryRequest, []cognitive.ExperienceResponse{item})
			archiveEvidence.Status = "archive_resurfaced"
			archiveEvidence.Reason = "explicit named archive trigger lookup"
			s.recordArchiveEvidence(archiveEvidence)
			perCallEvidence = append(perCallEvidence, cloneArchiveEvidenceEntry(archiveEvidence))
		}
		return item, perCallEvidence, true, nil
	}
	if archiveEvidencePending {
		archiveEvidence.EvidenceRefs = archiveTriggerEvidenceRefs(triggers)
		archiveEvidence.Returned = 0
		archiveEvidence.AntiApplicabilityBlocked = false
		archiveEvidence.Status = "archive_not_resurfaced"
		archiveEvidence.Reason = "explicit named archive trigger lookup returned no matching detail"
		s.recordArchiveEvidence(archiveEvidence)
		perCallEvidence = append(perCallEvidence, cloneArchiveEvidenceEntry(archiveEvidence))
	}
	return cognitive.ExperienceResponse{}, perCallEvidence, false, nil
}

func (s *Service) queryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []ArchiveEvidenceEntry, error) {
	if s == nil {
		return nil, nil, fmt.Errorf("experience service is not configured")
	}
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	default:
	}

	triggers, err := NormalizeArchiveTriggerClasses(request.ArchiveTriggerClasses)
	if err != nil {
		return nil, nil, err
	}
	limit := normalizeExperienceLimit(request.Limit, len(triggers) > 0)
	candidates := cloneResponses(s.candidates)
	var perCallEvidence []ArchiveEvidenceEntry
	var archiveEvidence ArchiveEvidenceEntry
	archiveEvidencePending := false
	archiveStart := -1
	archiveEnd := -1
	if len(triggers) > 0 {
		archiveLimit := archiveLimit(limit)
		archiveEvidence = ArchiveEvidenceEntry{
			TriggerClasses:  append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
			CallerPrincipal: strings.TrimSpace(request.Principal),
			Project:         strings.TrimSpace(request.Project),
			RequestedLimit:  archiveLimit,
			Status:          "archive_unavailable",
			Reason:          "named archive trigger supplied but archive source is not configured",
			EvidenceRefs:    archiveTriggerEvidenceRefs(triggers),
		}
		if s.archive != nil {
			archiveRequest := request
			archiveRequest.ArchiveTriggerClasses = triggers
			archiveRequest.Limit = archiveLimit
			archiveItems, err := s.archive.QueryArchiveExperience(ctx, archiveRequest, triggers, archiveLimit)
			if err != nil {
				archiveEvidence.Returned = 0
				archiveEvidence.ExperienceRetrievalRan = true
				archiveEvidence.AntiApplicabilityBlocked = false
				archiveEvidence.Status = "archive_error"
				archiveEvidence.Reason = "archive source returned error"
				if len(archiveEvidence.EvidenceRefs) == 0 {
					archiveEvidence.EvidenceRefs = archiveTriggerEvidenceRefs(triggers)
				}
				s.recordArchiveEvidence(archiveEvidence)
				return nil, []ArchiveEvidenceEntry{cloneArchiveEvidenceEntry(archiveEvidence)}, err
			}
			if len(archiveItems) > archiveLimit {
				archiveItems = archiveItems[:archiveLimit]
			}
			filteredArchiveItems := archiveItems[:0]
			for _, archiveItem := range archiveItems {
				item := cloneResponse(archiveItem)
				item.ArchiveTriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...)
				if !projectMatches(request.Project, item.SourceAttribution, item.Provenance) {
					continue
				}
				filteredArchiveItems = append(filteredArchiveItems, item)
			}
			archiveStart = len(candidates)
			candidates = append(candidates, filteredArchiveItems...)
			archiveEnd = len(candidates)
			archiveEvidence.ExperienceRetrievalRan = true
			archiveEvidencePending = true
		} else {
			s.recordArchiveEvidence(archiveEvidence)
			perCallEvidence = append(perCallEvidence, cloneArchiveEvidenceEntry(archiveEvidence))
		}
	}
	terms := requestTerms(request)
	type scoredCandidate struct {
		item  cognitive.ExperienceResponse
		score int
		index int
	}
	scored := make([]scoredCandidate, 0, len(candidates))
	for i, candidate := range candidates {
		if !projectMatches(request.Project, candidate.SourceAttribution, candidate.Provenance) {
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
	var finalArchiveItems []cognitive.ExperienceResponse
	for _, candidate := range scored {
		results = append(results, candidate.item)
		if archiveEvidencePending && candidate.index >= archiveStart && candidate.index < archiveEnd {
			finalArchiveItems = append(finalArchiveItems, candidate.item)
		}
	}
	if archiveEvidencePending {
		archiveEvidence.SessionIDs, archiveEvidence.EvidenceRefs = archiveEvidenceScope(finalArchiveItems)
		archiveEvidence.Returned = len(finalArchiveItems)
		archiveEvidence.AntiApplicabilityBlocked = archiveAntiApplicabilityBlocked(request, finalArchiveItems)
		if len(finalArchiveItems) == 0 {
			archiveEvidence.EvidenceRefs = archiveTriggerEvidenceRefs(triggers)
			archiveEvidence.Status = "archive_not_resurfaced"
			archiveEvidence.Reason = "explicit named archive trigger lookup returned no final results"
		} else {
			archiveEvidence.Status = "archive_resurfaced"
			archiveEvidence.Reason = "explicit named archive trigger lookup"
		}
		s.recordArchiveEvidence(archiveEvidence)
		perCallEvidence = append(perCallEvidence, cloneArchiveEvidenceEntry(archiveEvidence))
	}
	return results, perCallEvidence, nil
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
		out = append(out, cloneArchiveEvidenceEntry(entry))
	}
	return out
}

func cloneArchiveEvidenceEntry(entry ArchiveEvidenceEntry) ArchiveEvidenceEntry {
	entry.TriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), entry.TriggerClasses...)
	entry.SessionIDs = append([]string(nil), entry.SessionIDs...)
	entry.EvidenceRefs = append([]string(nil), entry.EvidenceRefs...)
	return entry
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

func archiveEvidenceScope(items []cognitive.ExperienceResponse) ([]string, []string) {
	var sessionIDs []string
	var evidenceRefs []string
	for _, item := range items {
		attributions := append([]cognitive.ExperienceSourceAttribution(nil), item.Provenance...)
		attributions = append(attributions, item.SourceAttribution...)
		for _, attribution := range attributions {
			sessionIDs = appendUniqueString(sessionIDs, attribution.SessionID)
			evidenceRefs = appendUniqueString(evidenceRefs, attributionEvidenceRef(attribution))
		}
	}
	return sessionIDs, evidenceRefs
}

func archiveAntiApplicabilityBlocked(request cognitive.ExperienceQueryRequest, items []cognitive.ExperienceResponse) bool {
	for _, item := range items {
		for _, anti := range item.AntiApplicability {
			if antiApplicabilityMatches(anti.Condition, request) {
				return true
			}
		}
	}
	return false
}

func archiveTriggerEvidenceRefs(triggers []cognitive.ExperienceArchiveTriggerClass) []string {
	refs := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		refs = appendUniqueString(refs, "archive_trigger:"+string(trigger))
	}
	return refs
}

func attributionEvidenceRef(attribution cognitive.ExperienceSourceAttribution) string {
	kind := strings.TrimSpace(attribution.Kind)
	id := strings.TrimSpace(attribution.ID)
	if kind != "" && id != "" {
		return kind + ":" + id
	}
	if sessionID := strings.TrimSpace(attribution.SessionID); sessionID != "" {
		return "agent_session_state:" + sessionID
	}
	return id
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

var allowedArchiveTriggerClasses = []cognitive.ExperienceArchiveTriggerClass{
	cognitive.ExperienceArchiveTriggerHistoricalWhy,
	cognitive.ExperienceArchiveTriggerRegressionOrRollback,
	cognitive.ExperienceArchiveTriggerRevisitOldDecision,
	cognitive.ExperienceArchiveTriggerSimilarPriorFailure,
	cognitive.ExperienceArchiveTriggerTemporalTruthChange,
	cognitive.ExperienceArchiveTriggerExplicitLookup,
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

func normalizeExperienceLimit(limit int, archiveTriggered bool) int {
	if archiveTriggered && limit <= 0 {
		return MaxArchiveResurfacingLimit
	}
	return normalizeLimit(limit)
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
	parts := []string{
		request.Query,
		request.CurrentContext,
		request.Situation,
		request.Decision,
		request.Action,
		request.Outcome,
		request.Revision,
		request.Reversal,
	}
	return uniqueTerms(strings.Join(parts, " "))
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
		candidate.Situation,
		candidate.Decision,
		candidate.Action,
		candidate.Outcome,
		candidate.Revision,
		candidate.Reversal,
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
	for _, attribution := range candidate.Provenance {
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
				State:            cognitive.ExperienceApplicabilityBlocked,
				Rationale:        rationale,
				DoesNotApplyWhen: []string{strings.TrimSpace(anti.Condition)},
				Confidence:       "high",
				BlockReason:      rationale,
				OverrideEvidence: "explicit operator or agent evidence is required before reusing this blocked experience",
			}
		}
	}
	if strings.TrimSpace(request.CurrentContext) == "" {
		return cognitive.ExperienceApplicability{
			State:           cognitive.ExperienceApplicabilityUncertain,
			Rationale:       "current_context is required before this experience can be silently reused",
			RequiredContext: []string{"current_context"},
			Confidence:      "low",
			BlockReason:     "missing current_context",
		}
	}
	if score <= 0 {
		return cognitive.ExperienceApplicability{
			State:      cognitive.ExperienceApplicabilityUncertain,
			Rationale:  "experience did not match the request context strongly enough for automatic reuse",
			Confidence: "low",
		}
	}
	if candidate.Applicability.State != "" && strings.TrimSpace(candidate.Applicability.Rationale) != "" {
		return ensureApplicabilityEnvelope(request, candidate, candidate.Applicability)
	}
	return ensureApplicabilityEnvelope(request, candidate, cognitive.ExperienceApplicability{
		State:     cognitive.ExperienceApplicabilityApplies,
		Rationale: "experience matched the request context and no anti-applicability condition matched",
	})
}

func ensureApplicabilityEnvelope(request cognitive.ExperienceQueryRequest, candidate cognitive.ExperienceResponse, applicability cognitive.ExperienceApplicability) cognitive.ExperienceApplicability {
	if applicability.Confidence == "" {
		switch applicability.State {
		case cognitive.ExperienceApplicabilityApplies:
			applicability.Confidence = "medium"
		case cognitive.ExperienceApplicabilityBlocked:
			applicability.Confidence = "high"
		default:
			applicability.Confidence = "low"
		}
	}
	if len(applicability.AppliesWhen) == 0 && applicability.State == cognitive.ExperienceApplicabilityApplies {
		applicability.AppliesWhen = applicabilityContext(request, candidate)
	}
	if len(applicability.DoesNotApplyWhen) == 0 {
		for _, anti := range candidate.AntiApplicability {
			condition := strings.TrimSpace(anti.Condition)
			if condition != "" {
				applicability.DoesNotApplyWhen = append(applicability.DoesNotApplyWhen, condition)
			}
		}
	}
	if applicability.State == cognitive.ExperienceApplicabilityBlocked && applicability.BlockReason == "" {
		applicability.BlockReason = strings.TrimSpace(applicability.Rationale)
	}
	return applicability
}

func applicabilityContext(request cognitive.ExperienceQueryRequest, candidate cognitive.ExperienceResponse) []string {
	contexts := []string{
		request.CurrentContext,
		request.Situation,
		candidate.Situation,
		candidate.Decision,
	}
	out := make([]string, 0, len(contexts))
	for _, contextValue := range contexts {
		contextValue = strings.TrimSpace(contextValue)
		if contextValue != "" && !slices.Contains(out, contextValue) {
			out = append(out, contextValue)
		}
	}
	return out
}

func antiApplicabilityMatches(condition string, request cognitive.ExperienceQueryRequest) bool {
	conditionTerms := uniqueTerms(condition)
	if len(conditionTerms) == 0 {
		return false
	}
	contextTerms := termSet(requestTerms(request))
	for _, term := range conditionTerms {
		if _, ok := contextTerms[term]; !ok {
			return false
		}
	}
	return true
}

func projectMatches(project string, attributionGroups ...[]cognitive.ExperienceSourceAttribution) bool {
	project = strings.TrimSpace(project)
	if project == "" {
		return true
	}
	for _, attributions := range attributionGroups {
		for _, attribution := range attributions {
			attrProject := strings.TrimSpace(attribution.Project)
			if attrProject == "" {
				continue
			}
			if attrProject == project {
				return true
			}
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
	if item.StorageOrigin == "" {
		item.StorageOrigin = item.Source
	}
	item.AntiApplicability = append([]cognitive.ExperienceAntiApplicability(nil), item.AntiApplicability...)
	sourceAttribution := append([]cognitive.ExperienceSourceAttribution(nil), item.SourceAttribution...)
	provenance := append([]cognitive.ExperienceSourceAttribution(nil), item.Provenance...)
	if len(sourceAttribution) == 0 && len(provenance) > 0 {
		sourceAttribution = append([]cognitive.ExperienceSourceAttribution(nil), provenance...)
	}
	if len(provenance) == 0 && len(sourceAttribution) > 0 {
		provenance = append([]cognitive.ExperienceSourceAttribution(nil), sourceAttribution...)
	}
	item.SourceAttribution = sourceAttribution
	item.Provenance = provenance
	item.ArchiveTriggerClasses = append([]cognitive.ExperienceArchiveTriggerClass(nil), item.ArchiveTriggerClasses...)
	return item
}
