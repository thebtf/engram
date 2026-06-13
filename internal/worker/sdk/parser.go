// Package sdk provides SDK agent integration for engram.
package sdk

import (
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/thebtf/engram/pkg/models"
)

// Package-level compiled regexes. Compiled once at init to avoid per-call overhead.
// The (?s) flag enables DOTALL so '.' crosses newline boundaries inside XML blocks.
var (
	// observationRegex extracts the inner content of each <observation>…</observation> block.
	observationRegex = regexp.MustCompile(`(?s)<observation>(.*?)</observation>`)

	// summaryRegex extracts the inner content of a <summary>…</summary> block.
	summaryRegex = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

	// skipSummaryRegex matches an explicit <skip_summary reason="…"/> directive.
	// When present, the session intentionally produced no summary (e.g. too early in session).
	skipSummaryRegex = regexp.MustCompile(`<skip_summary\s+reason="([^"]+)"\s*/>`)
)

// validObsTypes is the closed set of observation type strings the extraction
// prompt can emit. Anything outside this set falls back to "change".
var validObsTypes = map[string]bool{
	"bugfix":    true,
	"feature":   true,
	"refactor":  true,
	"change":    true,
	"discovery": true,
	"decision":  true,
	"guidance":  true,
}

// categoryTypeMap translates the structured category field (from the
// category-based extraction prompt, both live and backfill variants) into the
// canonical ObservationType used throughout the system. Category takes priority
// over the free-text <type> field when both are present.
var categoryTypeMap = map[string]models.ObservationType{
	"decision":      models.ObsTypeDecision,
	"correction":    models.ObsTypeDiscovery,  // corrections reveal user preferences
	"debugging":     models.ObsTypeBugfix,
	"gotcha":        models.ObsTypeDiscovery,
	"pattern":       models.ObsTypeDiscovery,
	"user_behavior": models.ObsTypeGuidance,
}

// validConcepts is the closed set of concept tags the system accepts.
// It covers both the standard semantic vocabulary and the GlobalizableConcepts
// set (concepts worth promoting to global scope in future releases).
// Any concept not in this set is logged and dropped before storage.
var validConcepts = map[string]bool{
	// Semantic concepts
	"how-it-works":     true,
	"why-it-exists":    true,
	"what-changed":     true,
	"problem-solution": true,
	"gotcha":           true,
	"pattern":          true,
	"trade-off":        true,
	// Globalizable concepts (from models.GlobalizableConcepts)
	"best-practice": true,
	"anti-pattern":  true,
	"architecture":  true,
	"security":      true,
	"performance":   true,
	"testing":       true,
	"debugging":     true,
	"workflow":      true,
	"tooling":       true,
	// Additional useful concepts
	"refactoring":    true,
	"api":            true,
	"database":       true,
	"configuration":  true,
	"error-handling": true,
	"caching":        true,
	"logging":        true,
	"auth":           true,
	"validation":     true,
}

// ParseObservations parses observation XML blocks from SDK response text.
// Each <observation>…</observation> block is decoded into a ParsedObservation.
// Invalid types and unknown concepts are logged and dropped rather than stored,
// keeping the knowledge graph clean.
func ParseObservations(text string, correlationID string) []*models.ParsedObservation {
	var observations []*models.ParsedObservation

	matches := observationRegex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		obsContent := match[1]

		// Extract all fields from the observation block.
		category := extractField(obsContent, "category")
		obsType := extractField(obsContent, "type")
		title := extractField(obsContent, "title")
		subtitle := extractField(obsContent, "subtitle")
		narrative := extractField(obsContent, "narrative")
		facts := extractArrayElements(obsContent, "facts", "fact")
		concepts := extractArrayElements(obsContent, "concepts", "concept")
		filesRead := extractArrayElements(obsContent, "files_read", "file")
		filesModified := extractArrayElements(obsContent, "files_modified", "file")
		commandsRun := extractArrayElements(obsContent, "commands_run", "command")

		// Determine final type: category mapping takes precedence over <type> field.
		// This ensures category-based extraction produces the correct observation type.
		var finalType models.ObservationType
		var finalSourceType models.SourceType
		if mappedType, ok := categoryTypeMap[category]; ok {
			finalType = mappedType
			if category == "user_behavior" {
				finalSourceType = models.SourceLLMDerived
			}
		} else {
			// No category or unknown category: fall back to <type> field
			finalType = models.ObsTypeChange
			if obsType != "" {
				if validObsTypes[obsType] {
					finalType = models.ObservationType(obsType)
				} else {
					log.Warn().
						Str("correlationId", correlationID).
						Str("invalidType", obsType).
						Msg("Invalid observation type, using 'change'")
				}
			} else {
				log.Warn().
					Str("correlationId", correlationID).
					Msg("Observation missing type and category fields, using 'change'")
			}
		}

		// Filter concepts: only keep valid ones from the strict list
		cleanedConcepts := make([]string, 0, len(concepts))
		var invalidConcepts []string
		for _, c := range concepts {
			c = strings.ToLower(strings.TrimSpace(c))
			if c == string(finalType) {
				continue // Skip type in concepts
			}
			if validConcepts[c] {
				cleanedConcepts = append(cleanedConcepts, c)
			} else {
				invalidConcepts = append(invalidConcepts, c)
			}
		}
		if len(invalidConcepts) > 0 {
			log.Warn().
				Str("correlationId", correlationID).
				Strs("invalidConcepts", invalidConcepts).
				Msg("Filtered out invalid concepts (not in allowed list)")
		}

		observations = append(observations, &models.ParsedObservation{
			Type:          finalType,
			SourceType:    finalSourceType,
			Title:         title,
			Subtitle:      subtitle,
			Facts:         facts,
			Narrative:     narrative,
			Concepts:      cleanedConcepts,
			FilesRead:     filesRead,
			FilesModified: filesModified,
			CommandsRun:   commandsRun,
		})
	}

	return observations
}

// ParseSummary parses a summary XML block from SDK response text.
// Returns nil when the agent explicitly skipped the summary (skip_summary directive)
// or when no summary block is present in the response.
func ParseSummary(text string, sessionID int64) *models.ParsedSummary {
	// A skip_summary directive means the agent intentionally produced no summary.
	if skipMatch := skipSummaryRegex.FindStringSubmatch(text); skipMatch != nil {
		log.Info().
			Int64("sessionId", sessionID).
			Str("reason", skipMatch[1]).
			Msg("Summary skipped")
		return nil
	}

	// Find summary block
	match := summaryRegex.FindStringSubmatch(text)
	if len(match) < 2 {
		return nil
	}

	summaryContent := match[1]

	return &models.ParsedSummary{
		Request:      extractField(summaryContent, "request"),
		Investigated: extractField(summaryContent, "investigated"),
		Learned:      extractField(summaryContent, "learned"),
		Completed:    extractField(summaryContent, "completed"),
		NextSteps:    extractField(summaryContent, "next_steps"),
		Notes:        extractField(summaryContent, "notes"),
	}
}

// extractField pulls the text content of a simple XML element from a content string.
// The pattern is compiled per-call; fields are called rarely enough that this
// does not show up in profiling.
func extractField(content, fieldName string) string {
	pattern := regexp.MustCompile(`<` + fieldName + `>([^<]*)</` + fieldName + `>`)
	match := pattern.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// extractArrayElements pulls all <elementName>…</elementName> children from
// within a named <arrayName>…</arrayName> block. Returns nil (not empty slice)
// when the outer block is absent, which signals "field not present" to callers.
func extractArrayElements(content, arrayName, elementName string) []string {
	var elements []string

	// Locate the wrapping array block first.
	arrayPattern := regexp.MustCompile(`(?s)<` + arrayName + `>(.*?)</` + arrayName + `>`)
	arrayMatch := arrayPattern.FindStringSubmatch(content)
	if len(arrayMatch) < 2 {
		return elements
	}

	arrayContent := arrayMatch[1]

	// Extract individual elements from within the array block.
	elementPattern := regexp.MustCompile(`<` + elementName + `>([^<]+)</` + elementName + `>`)
	elementMatches := elementPattern.FindAllStringSubmatch(arrayContent, -1)
	for _, match := range elementMatches {
		if len(match) >= 2 {
			elements = append(elements, strings.TrimSpace(match[1]))
		}
	}

	return elements
}
