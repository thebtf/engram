package aaak

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// stopWords is a set of common English words to exclude from topic extraction.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "by": true, "from": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "must": true,
	"shall": true, "can": true, "need": true, "dare": true, "ought": true,
	"this": true, "that": true, "these": true, "those": true, "it": true,
	"its": true, "i": true, "me": true, "my": true, "we": true, "our": true,
	"you": true, "your": true, "he": true, "she": true, "they": true, "them": true,
	"not": true, "no": true, "nor": true, "as": true, "if": true, "then": true,
	"so": true, "up": true, "out": true, "about": true, "into": true, "over": true,
	"after": true, "before": true, "between": true, "under": true, "again": true,
	"there": true, "here": true, "when": true, "where": true, "why": true,
	"how": true, "all": true, "each": true, "every": true, "both": true,
	"few": true, "more": true, "most": true, "other": true, "some": true,
	"such": true, "than": true, "too": true, "very": true, "just": true,
	"also": true, "now": true, "only": true, "which": true, "who": true,
	"what": true, "their": true, "his": true, "her": true,
}

// camelCaseRE matches camelCase or PascalCase identifiers.
var camelCaseRE = regexp.MustCompile(`[A-Z][a-z]+(?:[A-Z][a-z]+)+`)

// properNounRE matches words starting with uppercase (not at sentence start).
var properNounRE = regexp.MustCompile(`(?:^|\. )([A-Z][a-z]{2,})`)

// hyphenatedRE matches hyphenated compound terms.
var hyphenatedRE = regexp.MustCompile(`\b[a-zA-Z]+-[a-zA-Z]+\b`)

type scoredTopic struct {
	term  string
	score int
}

// ExtractTopics returns the top-N frequency-ranked non-stopword tokens from text.
// Boosts: proper nouns (+3), CamelCase (+2), hyphenated terms (+1).
func ExtractTopics(text string, maxTopics int) []string {
	if text == "" || maxTopics <= 0 {
		return nil
	}

	scores := make(map[string]int)

	// Count word frequencies (lowercase, skip stopwords and short words)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	})
	for _, w := range words {
		lower := strings.ToLower(w)
		if len(lower) < 3 || stopWords[lower] {
			continue
		}
		scores[lower]++
	}

	// Boost CamelCase identifiers
	for _, match := range camelCaseRE.FindAllString(text, -1) {
		lower := strings.ToLower(match)
		scores[lower] += 2
	}

	// Boost proper nouns
	for _, match := range properNounRE.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			lower := strings.ToLower(match[1])
			if !stopWords[lower] {
				scores[lower] += 3
			}
		}
	}

	// Boost hyphenated terms
	for _, match := range hyphenatedRE.FindAllString(text, -1) {
		lower := strings.ToLower(match)
		scores[lower]++
	}

	// Sort by score descending
	topics := make([]scoredTopic, 0, len(scores))
	for term, score := range scores {
		topics = append(topics, scoredTopic{term, score})
	}
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].score != topics[j].score {
			return topics[i].score > topics[j].score
		}
		return topics[i].term < topics[j].term // stable sort for equal scores
	})

	result := make([]string, 0, maxTopics)
	for i := 0; i < len(topics) && i < maxTopics; i++ {
		result = append(result, topics[i].term)
	}
	return result
}
