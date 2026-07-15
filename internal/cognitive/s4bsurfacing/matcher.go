package s4bsurfacing

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/thebtf/engram/internal/cognitive/s4directives"
	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	proposalSource = "s4b.directive"
	proposalReason = "Related confirmed creator directive"
	maxTitleRunes  = 80
)

var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"by": {}, "for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {},
	"on": {}, "or": {}, "that": {}, "the": {}, "this": {}, "to": {}, "with": {},
}

type matchContext struct {
	eventType string
	project   string
	session   string
	tokens    map[string]struct{}
}

type matchedDirective struct {
	event     s4directives.StoredAttentionEvent
	eventType string
	overlap   int
	score     float64
	title     string
}

func newMatchContext(event cognitive.AttentionEvent) matchContext {
	return matchContext{
		eventType: normalizeEventType(event.Type),
		project:   strings.TrimSpace(event.Project),
		session:   strings.TrimSpace(event.SessionID),
		tokens:    tokenize(attentionText(event.Payload)),
	}
}

func attentionText(payload map[string]interface{}) string {
	for _, key := range []string{"text", "topic", "query"} {
		value, ok := payload[key].(string)
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizeEventType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return ""
	}
	return value
}

func matchDirective(ctx matchContext, event s4directives.StoredAttentionEvent) (matchedDirective, bool) {
	project := strings.TrimSpace(event.Project)
	session := strings.TrimSpace(event.SessionID)
	horizon := strings.ToLower(strings.TrimSpace(event.Horizon))
	privacy := strings.ToLower(strings.TrimSpace(event.PrivacyClass))
	title := strings.TrimSpace(event.DerivedIntent)

	if !event.AgentConfirmed || event.ID <= 0 || event.CreatedAt.IsZero() || title == "" || project != ctx.project || session == "" {
		return matchedDirective{}, false
	}
	if !validHorizon(horizon) || !validPrivacy(privacy) {
		return matchedDirective{}, false
	}
	sameSession := ctx.session != "" && session == ctx.session
	if horizon == "session" && !sameSession {
		return matchedDirective{}, false
	}
	if privacy == "secret" && !sameSession {
		return matchedDirective{}, false
	}

	score, overlap := lexicalOverlap(ctx.tokens, tokenize(title))
	if overlap == 0 {
		return matchedDirective{}, false
	}

	return matchedDirective{event: event, eventType: ctx.eventType, overlap: overlap, score: score, title: boundTitle(title)}, true
}

func validHorizon(value string) bool {
	switch value {
	case "session", "project", "permanent":
		return true
	default:
		return false
	}
}

func validPrivacy(value string) bool {
	switch value {
	case "public", "internal", "secret":
		return true
	default:
		return false
	}
}

func tokenize(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if _, skip := stopwords[field]; !skip && field != "" {
			tokens[field] = struct{}{}
		}
	}
	return tokens
}

func lexicalOverlap(left, right map[string]struct{}) (float64, int) {
	if len(left) == 0 || len(right) == 0 {
		return 0, 0
	}
	overlap := 0
	for token := range left {
		if _, ok := right[token]; ok {
			overlap++
		}
	}
	if overlap == 0 {
		return 0, 0
	}
	union := len(left) + len(right) - overlap
	return float64(overlap) / float64(union), overlap
}

func sortMatches(matches []matchedDirective) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if !matches[i].event.CreatedAt.Equal(matches[j].event.CreatedAt) {
			return matches[i].event.CreatedAt.After(matches[j].event.CreatedAt)
		}
		return matches[i].event.ID < matches[j].event.ID
	})
}

func (m matchedDirective) proposal() cognitive.HintProposal {
	return cognitive.HintProposal{
		ID:        fmt.Sprintf("s4b:directive:%d", m.event.ID),
		Title:     m.title,
		CreatedAt: m.event.CreatedAt,
		Score:     float32(m.score),
		Source:    proposalSource,
		Reason:    fmt.Sprintf("%s (event_type=%s, overlap_tokens=%d)", proposalReason, m.eventType, m.overlap),
	}
}

func boundTitle(title string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) > maxTitleRunes {
		runes = runes[:maxTitleRunes]
	}
	return string(runes)
}
